package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/ratelimit"
	"p2p-signaling-server/internal/room"
)

const (
	helloTimeout  = 10 * time.Second
	helloMaxBytes = 4096
	pingInterval  = 20 * time.Second
	writeTimeout  = 10 * time.Second
	outQueue      = 64
	maxMetaBytes  = 1024
	maxNameBytes  = 32

	// connectsPerIPPerMinute caps new WebSocket connections per address. It is generous because
	// many players can share one address (LAN, school, mobile carrier NAT).
	connectsPerIPPerMinute = 120

	defaultPendingTTL    = 60 * time.Second
	defaultIdleTimeout   = 10 * time.Minute
	defaultSweepInterval = 5 * time.Second
)

// Handler serves GET /ws.
type Handler struct {
	Buckets *bucket.Service
	Rooms   *room.Manager
	Log     *slog.Logger
	Metrics *metrics.Metrics // optional

	// ClientIP resolves the real client address (see httpserver.ClientIP). Defaults to the
	// connection's remote address.
	ClientIP func(*http.Request) netip.Addr
	// PendingTTL is how long a joiner may wait for host approval. Default 60s.
	PendingTTL time.Duration

	once     sync.Once
	connects *ratelimit.Limiter
	joins    *ratelimit.Limiter
	lockouts *ratelimit.Lockout
}

func (h *Handler) init() {
	h.once.Do(func() {
		if h.ClientIP == nil {
			h.ClientIP = func(r *http.Request) netip.Addr {
				ap, _ := netip.ParseAddrPort(r.RemoteAddr)
				return ap.Addr().Unmap()
			}
		}
		if h.PendingTTL == 0 {
			h.PendingTTL = defaultPendingTTL
		}
		h.connects, h.joins, h.lockouts = ratelimit.NewLimiter(), ratelimit.NewLimiter(), ratelimit.NewLockout()
	})
}

type conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	name   string
	ip     netip.Addr
}

// send queues a message; a client that cannot keep up is disconnected.
func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.out <- b:
	default:
		c.cancel()
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	ip := h.ClientIP(r)
	if !h.connects.Allow(ip.String(), connectsPerIPPerMinute) {
		h.Metrics.RateLimited()
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin is checked per bucket after hello
	})
	if err != nil {
		return
	}
	h.Metrics.Connection()
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, outQueue), ip: ip}
	defer ws.CloseNow()
	defer cancel()

	ws.SetReadLimit(helloMaxBytes)
	b, ok := h.hello(c, r)
	if !ok {
		return
	}
	ws.SetReadLimit(int64(b.Limits.MessageBytes))

	go c.writeLoop()
	go c.pingLoop()

	p := &room.Peer{Send: c.send, IP: ip}
	defer h.leave(p)
	h.readLoop(c, p, b)
}

// Run sweeps idle rooms and expired join requests until ctx ends.
func (h *Handler) Run(ctx context.Context, interval time.Duration) {
	h.init()
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.sweep()
		}
	}
}

func (h *Handler) sweep() {
	res := h.Rooms.Sweep(func(bucketID string) time.Duration {
		if b, ok := h.Buckets.Get(bucketID); ok {
			return time.Duration(b.RoomIdleTimeoutSeconds) * time.Second
		}
		return defaultIdleTimeout
	}, h.PendingTTL)
	for _, peers := range res.Closed {
		for _, p := range peers {
			p.Send(roomClosed{"room_closed", "idle_timeout"})
		}
	}
	for _, d := range res.Denied {
		d.Peer.Send(newError(ErrJoinDenied, "the host did not respond in time"))
		d.Host.Send(joinCancelled{"join_cancelled", d.Peer.ID})
	}
}

// hello performs the handshake. Errors are written directly, then the socket is closed.
func (h *Handler) hello(c *conn, r *http.Request) (bucket.Bucket, bool) {
	fail := func(code, msg string) (bucket.Bucket, bool) {
		wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
		defer cancel()
		data, _ := json.Marshal(newError(code, msg))
		_ = c.ws.Write(wctx, websocket.MessageText, data)
		_ = c.ws.Close(websocket.StatusPolicyViolation, code)
		return bucket.Bucket{}, false
	}
	rctx, cancel := context.WithTimeout(c.ctx, helloTimeout)
	defer cancel()
	_, raw, err := c.ws.Read(rctx)
	if err != nil {
		return bucket.Bucket{}, false
	}
	t, msg, err := decode(raw)
	hi, isHello := msg.(*hello)
	if err != nil || t != "hello" || !isHello {
		return fail(ErrBadMessage, "first message must be a valid hello")
	}
	if hi.Protocol != ProtocolVersion {
		return fail(ErrUnsupportedProt, "unsupported protocol version")
	}
	b, found := h.Buckets.Get(hi.Bucket)
	if !found {
		return fail(ErrBucketNotFound, "unknown bucket")
	}
	if !b.Enabled {
		return fail(ErrBucketDisabled, "bucket is disabled")
	}
	if !b.VersionAccepted(hi.GameVersion) {
		return fail(ErrVersionRejected, "game version not accepted")
	}
	if origin := r.Header.Get("Origin"); origin != "" && len(b.AllowedOrigins) > 0 && !slices.Contains(b.AllowedOrigins, origin) {
		return fail(ErrOriginRejected, "origin not allowed")
	}
	policy := "all"
	if b.RelayOnly {
		policy = "relay"
	}
	data, _ := json.Marshal(welcome{
		T: "welcome", IceServers: b.IceServers, IcePolicy: policy, Topology: b.Topology, Protocol: ProtocolVersion,
		Limits: welcomeLims{b.Limits.MessageBytes, b.Limits.MessagesPerSecond, b.MaxPlayers},
	})
	wctx, wcancel := context.WithTimeout(c.ctx, writeTimeout)
	defer wcancel()
	if err := c.ws.Write(wctx, websocket.MessageText, data); err != nil {
		return bucket.Bucket{}, false
	}
	c.name = cleanName(hi.Name)
	return b, true
}

// cleanName keeps a player-supplied name short, valid UTF-8 and free of control characters.
func cleanName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		if sb.Len()+utf8.RuneLen(r) > maxNameBytes {
			break
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(sb.String())
}

func (c *conn) writeLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *conn) pingLoop() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (h *Handler) readLoop(c *conn, p *room.Peer, b bucket.Bucket) {
	lim := rate.NewLimiter(rate.Limit(b.Limits.MessagesPerSecond), b.Limits.MessagesPerSecond*2)
	for {
		_, raw, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if !lim.Allow() {
			h.Metrics.RateLimited()
			c.send(newError(ErrRateLimited, "too many messages"))
			c.cancel()
			return
		}
		h.Rooms.Touch(p) // any message (including ping) keeps the room alive
		_, msg, err := decode(raw)
		if err != nil {
			c.send(newError(ErrBadMessage, err.Error()))
			continue
		}
		switch m := msg.(type) {
		case *createRoom:
			h.create(c, p, b, m)
		case *joinRoom:
			h.join(c, p, b, m)
		case *approveMsg:
			h.approve(c, p, m)
		case *lockMsg:
			h.lock(c, p, m)
		case *signalIn:
			h.signal(c, p, m)
		case *bare:
			if m.T == "ping" {
				c.send(simple{"pong"})
			} else {
				h.leave(p)
			}
		default:
			c.send(newError(ErrBadMessage, "unexpected message"))
		}
	}
}

func (h *Handler) create(c *conn, p *room.Peer, b bucket.Bucket, m *createRoom) {
	if len(m.Meta) > maxMetaBytes {
		c.send(newError(ErrBadMessage, "meta too large"))
		return
	}
	maxPlayers := b.MaxPlayers
	if m.MaxPlayers != 0 {
		if m.MaxPlayers < 2 || m.MaxPlayers > b.MaxPlayers {
			c.send(newError(ErrBadMessage, "max_players out of range"))
			return
		}
		maxPlayers = m.MaxPlayers
	}
	p.Name = c.name
	r, err := h.Rooms.Create(b.ID, room.Params{
		Topology: b.Topology, MaxPlayers: maxPlayers, MaxRooms: b.MaxRooms,
		CodeLength: b.CodeLength, RoomsPerIP: b.Limits.RoomsPerIP,
	}, p)
	switch {
	case errors.Is(err, room.ErrBusy):
		c.send(newError(ErrServerBusy, "no room capacity"))
	case errors.Is(err, room.ErrTooManyRooms):
		h.Metrics.RateLimited()
		c.send(newError(ErrRateLimited, "too many rooms from your address"))
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
	default:
		h.Metrics.RoomCreated()
		c.send(roomCreated{"room_created", r.Code, p.ID})
	}
}

func (h *Handler) join(c *conn, p *room.Peer, b bucket.Bucket, m *joinRoom) {
	// Lockout and rate limit apply before the code is even looked at, so a guesser learns nothing
	// once blocked, and a correct code does not bypass them.
	key := b.ID + "|" + c.ip.String()
	if h.lockouts.Locked(key) {
		h.Metrics.RateLimited()
		c.send(newError(ErrLockedOut, "too many failed attempts, try again later"))
		return
	}
	if !h.joins.Allow(key, b.Limits.JoinsPerIPPerMinute) {
		h.Metrics.RateLimited()
		c.send(newError(ErrRateLimited, "too many join attempts"))
		return
	}
	code := strings.ToUpper(strings.TrimSpace(m.Code))
	p.Name = c.name
	res, err := h.Rooms.Join(b.ID, code, p, b.HostApproval)
	switch {
	case errors.Is(err, room.ErrNotFound):
		h.Metrics.FailedJoin()
		lock := time.Duration(b.Limits.LockoutSeconds) * time.Second
		if h.lockouts.Fail(key, b.Limits.FailedJoinsBeforeLockout, lock) {
			h.Metrics.Lockout()
		}
		c.send(newError(ErrRoomNotFound, "room not found"))
		return
	case errors.Is(err, room.ErrLocked):
		c.send(newError(ErrRoomLocked, "room is locked"))
		return
	case errors.Is(err, room.ErrFull):
		c.send(newError(ErrRoomFull, "room is full"))
		return
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
		return
	}
	if res.Pending {
		c.send(simple{"join_pending"})
		res.Room.Host().Send(joinRequest{"join_request", p.ID, p.Name})
		return
	}
	h.admitted(res.Room, p, res.Existing)
}

// admitted tells the joiner it is in and tells the right existing peers about it.
func (h *Handler) admitted(r *room.Room, p *room.Peer, existing []*room.Peer) {
	h.Metrics.Join()
	ids := []int32{room.HostID}
	if r.Topology == "mesh" {
		ids = ids[:0]
		for _, o := range existing {
			ids = append(ids, o.ID)
		}
	}
	p.Send(joined{"joined", p.ID, room.HostID, ids})
	for _, o := range existing {
		if r.Topology == "mesh" || o == r.Host() {
			o.Send(peerEvent{"peer_joined", p.ID})
		}
	}
}

func (h *Handler) approve(c *conn, p *room.Peer, m *approveMsg) {
	if m.Accept == nil {
		c.send(newError(ErrBadMessage, "accept is required"))
		return
	}
	res, err := h.Rooms.Approve(p, m.Peer, *m.Accept)
	switch {
	case errors.Is(err, room.ErrNotHost), errors.Is(err, room.ErrNotInRoom):
		c.send(newError(ErrNotHost, "only a host can approve joins"))
	case errors.Is(err, room.ErrNoPeer):
		c.send(newError(ErrBadMessage, "no pending join from that peer"))
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
	case res.Accepted:
		h.admitted(res.Room, res.Peer, res.Existing)
	default:
		res.Peer.Send(newError(ErrJoinDenied, "the host declined your request"))
	}
}

func (h *Handler) lock(c *conn, p *room.Peer, m *lockMsg) {
	if m.Locked == nil {
		c.send(newError(ErrBadMessage, "locked is required"))
		return
	}
	if err := h.Rooms.SetLocked(p, *m.Locked); err != nil {
		c.send(newError(ErrNotHost, "only a host can lock the room"))
	}
}

func (h *Handler) signal(c *conn, p *room.Peer, m *signalIn) {
	switch m.Kind {
	case "offer", "answer", "candidate":
	default:
		c.send(newError(ErrBadMessage, "invalid signal kind"))
		return
	}
	if len(m.Data) == 0 {
		c.send(newError(ErrBadMessage, "signal data required"))
		return
	}
	target, err := h.Rooms.Route(p, m.To)
	if err != nil {
		c.send(newError(ErrBadMessage, err.Error()))
		return
	}
	target.Send(signalOut{"signal", p.ID, m.Kind, m.Data})
}

// leave removes p from its room and notifies the others.
func (h *Handler) leave(p *room.Peer) {
	res, ok := h.Rooms.Leave(p)
	if !ok {
		return
	}
	switch {
	case res.Closed:
		for _, o := range res.Others {
			o.Send(roomClosed{"room_closed", "host_left"})
		}
	case res.Pending:
		for _, o := range res.Others {
			o.Send(joinCancelled{"join_cancelled", res.Left})
		}
	default:
		for _, o := range res.Others {
			if res.Room.Topology == "mesh" || o == res.Room.Host() {
				o.Send(peerEvent{"peer_left", res.Left})
			}
		}
	}
}

// CloseBucket closes every live room of a bucket (e.g. when the bucket is deleted).
func (h *Handler) CloseBucket(id, reason string) {
	for _, p := range h.Rooms.CloseBucket(id) {
		p.Send(roomClosed{"room_closed", reason})
	}
}

// CloseRoom closes one room; it reports whether the room existed.
func (h *Handler) CloseRoom(bucketID, code, reason string) bool {
	ps, ok := h.Rooms.CloseRoom(bucketID, code)
	for _, p := range ps {
		p.Send(roomClosed{"room_closed", reason})
	}
	return ok
}
