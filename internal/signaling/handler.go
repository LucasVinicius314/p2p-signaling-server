package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/netutil"
	"p2p-signaling-server/internal/ratelimit"
	"p2p-signaling-server/internal/room"
)

const (
	helloTimeout  = 10 * time.Second
	helloMaxBytes = 4096
	pingInterval  = 20 * time.Second
	writeTimeout  = 10 * time.Second
	outQueue      = 64
	maxNameBytes  = 32

	// connectsPerIPPerMinute caps new WebSocket connections per address. It is generous because
	// many players can share one address (LAN, school, mobile carrier NAT).
	connectsPerIPPerMinute = 120

	// Concurrent connections are capped too: the rate above only limits how fast connections open, not
	// how many stay open. Defaults for Handler.MaxConnsPerIP and Handler.MaxConns.
	defaultMaxConnsPerIP = 32
	defaultMaxConns      = 10000

	defaultPendingTTL    = 60 * time.Second
	defaultIdleTimeout   = 10 * time.Minute
	defaultSweepInterval = 5 * time.Second

	// flushWait is how long a connection that is about to be closed gets to deliver its queued messages.
	flushWait = 500 * time.Millisecond
)

// Handler serves GET /ws.
type Handler struct {
	Buckets *bucket.Service
	Rooms   *room.Manager
	Log     *slog.Logger     // optional; nothing is logged when nil
	Metrics *metrics.Metrics // optional

	// ClientIP resolves the real client address (see httpserver.ClientIP). Defaults to the
	// connection's remote address.
	ClientIP func(*http.Request) netip.Addr
	// PendingTTL is how long a joiner may wait for host approval. Default 60s.
	PendingTTL time.Duration
	// MaxConnsPerIP and MaxConns cap concurrent connections per address (an IPv6 /64 counts as one) and in
	// total. Defaults 32 and 10000.
	MaxConnsPerIP int
	MaxConns      int

	once     sync.Once
	connects *ratelimit.Limiter
	joins    *ratelimit.Limiter
	failures *ratelimit.Limiter // wrong codes per bucket, all addresses together
	lockouts *ratelimit.Lockout

	draining atomic.Bool

	mu       sync.Mutex
	conns    map[*conn]struct{} // sockets that exist, for Shutdown
	total    int                // reserved connection slots, all addresses
	perKey   map[string]int     // reserved connection slots per address key
	logOnce  sync.Once
	logValue *slog.Logger
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
		if h.MaxConnsPerIP == 0 {
			h.MaxConnsPerIP = defaultMaxConnsPerIP
		}
		if h.MaxConns == 0 {
			h.MaxConns = defaultMaxConns
		}
		h.connects, h.joins, h.failures = ratelimit.NewLimiter(), ratelimit.NewLimiter(), ratelimit.NewLimiter()
		h.lockouts = ratelimit.NewLockout()
		h.conns, h.perKey = map[*conn]struct{}{}, map[string]int{}
	})
}

// log returns the logger, or one that discards everything when none was set.
func (h *Handler) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	h.logOnce.Do(func() { h.logValue = slog.New(slog.NewTextHandler(io.Discard, nil)) })
	return h.logValue
}

type conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	name   string
	ip     netip.Addr
	key    string // rate limit identity of ip (netutil.Key)

	ready       atomic.Bool // hello done: the writer goroutine is running
	failedJoins int         // wrong codes tried on this connection; readLoop goroutine only
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

// flush waits briefly for queued messages to be written, so a message sent right before a close arrives.
func (c *conn) flush() {
	deadline := time.Now().Add(flushWait)
	for len(c.out) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // the last message may still be mid-write
}

// acquire reserves a connection slot for key, or reports that a cap is reached.
func (h *Handler) acquire(key string) (release func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= h.MaxConns || h.perKey[key] >= h.MaxConnsPerIP {
		return nil, false
	}
	h.total++
	h.perKey[key]++
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.total--
		if h.perKey[key]--; h.perKey[key] <= 0 {
			delete(h.perKey, key)
		}
	}, true
}

func (h *Handler) track(c *conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Handler) untrack(c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	if h.draining.Load() {
		http.Error(w, "server restarting", http.StatusServiceUnavailable)
		return
	}
	ip := h.ClientIP(r)
	key := netutil.Key(ip)
	if !h.connects.Allow(key, connectsPerIPPerMinute) {
		h.Metrics.RateLimited()
		h.log().Debug("connection refused: too many new connections", "reason", "connect_rate")
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	release, ok := h.acquire(key)
	if !ok {
		h.Metrics.RateLimited()
		h.log().Debug("connection refused: too many open connections", "reason", "connection_cap")
		http.Error(w, "too many open connections", http.StatusTooManyRequests)
		return
	}
	defer release()

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin is checked per bucket after hello
	})
	if err != nil {
		return
	}
	h.Metrics.Connection()
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, outQueue), ip: ip, key: key}
	h.track(c)
	defer h.untrack(c)
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
	c.ready.Store(true)

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

// Shutdown stops accepting new connections, tells every connected client that the server is restarting
// (so it can retry after retryAfter) and closes their sockets. Games whose peers already have their
// peer-to-peer links keep running; only signaling is interrupted. It returns when every socket is closed
// or ctx ends.
func (h *Handler) Shutdown(ctx context.Context, retryAfter time.Duration) {
	h.init()
	h.draining.Store(true)
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	h.log().Info("draining signaling connections", "connections", len(conns))

	msg := serverRestarting{"server_restarting", retryAfter.Milliseconds()}
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.ready.Load() { // before hello there is no writer; the socket is simply closed
				c.send(msg)
				c.flush()
			}
			_ = c.ws.Close(websocket.StatusServiceRestart, "server restarting")
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
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
		h.log().Info("room closed", "reason", "idle_timeout", "peers", len(peers))
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
	fail := func(code, msg string, attrs ...any) (bucket.Bucket, bool) {
		h.log().Info("hello rejected", append([]any{"code", code}, attrs...)...)
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
		return fail(ErrBucketNotFound, "unknown bucket", "bucket", clip(hi.Bucket, 40))
	}
	if !b.Enabled {
		return fail(ErrBucketDisabled, "bucket is disabled", "bucket", b.ID)
	}
	if origin := r.Header.Get("Origin"); origin != "" && len(b.AllowedOrigins) > 0 && !slices.Contains(b.AllowedOrigins, origin) {
		return fail(ErrOriginRejected, "origin not allowed", "bucket", b.ID)
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

// clip shortens an untrusted string before it is logged.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
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
			h.log().Debug("connection dropped: message rate", "bucket", b.ID)
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
			if h.join(c, p, b, m) {
				// Too many wrong codes on this connection: say so, then drop it.
				c.flush()
				_ = c.ws.Close(websocket.StatusPolicyViolation, "too_many_failed_joins")
				return
			}
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
		h.log().Warn("room refused: bucket is at capacity", "bucket", b.ID)
		c.send(newError(ErrServerBusy, "no room capacity"))
	case errors.Is(err, room.ErrTooManyRooms):
		h.Metrics.RateLimited()
		c.send(newError(ErrRateLimited, "too many rooms from your address"))
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
	default:
		h.Metrics.RoomCreated()
		h.log().Info("room created", "bucket", b.ID, "topology", b.Topology)
		c.send(roomCreated{"room_created", r.Code, p.ID})
	}
}

// join handles join_room. It reports true when the connection must be dropped for guessing codes.
func (h *Handler) join(c *conn, p *room.Peer, b bucket.Bucket, m *joinRoom) (drop bool) {
	// Lockout and rate limits apply before the code is even looked at, so a guesser learns nothing
	// once blocked, and a correct code does not bypass them.
	key := b.ID + "|" + c.key
	if h.lockouts.Locked(key) {
		h.Metrics.RateLimited()
		c.send(newError(ErrLockedOut, "too many failed attempts, try again later"))
		return false
	}
	if !h.joins.Allow(key, b.Limits.JoinsPerIPPerMinute) {
		h.Metrics.RateLimited()
		c.send(newError(ErrRateLimited, "too many join attempts"))
		return false
	}
	// Backstop against guessing spread over many addresses: the bucket as a whole tolerates only so many
	// wrong codes per minute.
	if !h.failures.Available(b.ID, b.Limits.FailedJoinsPerMinute) {
		h.Metrics.RateLimited()
		h.log().Debug("join refused: bucket failed-join budget is spent", "bucket", b.ID)
		c.send(newError(ErrRateLimited, "too many failed joins on this server, try again shortly"))
		return false
	}
	code := strings.ToUpper(strings.TrimSpace(m.Code))
	p.Name = c.name
	res, err := h.Rooms.Join(b.ID, code, p, b.HostApproval)
	switch {
	case errors.Is(err, room.ErrNotFound):
		h.Metrics.FailedJoin()
		h.failures.Allow(b.ID, b.Limits.FailedJoinsPerMinute) // spend one unit of the bucket budget
		lock := time.Duration(b.Limits.LockoutSeconds) * time.Second
		if h.lockouts.Fail(key, b.Limits.FailedJoinsBeforeLockout, lock) {
			h.Metrics.Lockout()
			h.log().Warn("address locked out after failed joins", "bucket", b.ID, "seconds", b.Limits.LockoutSeconds)
		}
		c.failedJoins++
		c.send(newError(ErrRoomNotFound, "room not found"))
		if c.failedJoins >= b.Limits.FailedJoinsPerConnection {
			h.log().Debug("connection dropped: too many wrong codes", "bucket", b.ID)
			return true
		}
		return false
	case errors.Is(err, room.ErrLocked):
		c.send(newError(ErrRoomLocked, "room is locked"))
		return false
	case errors.Is(err, room.ErrFull):
		c.send(newError(ErrRoomFull, "room is full"))
		return false
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
		return false
	}
	if res.Pending {
		c.send(simple{"join_pending"})
		res.Room.Host().Send(joinRequest{"join_request", p.ID, p.Name})
		return false
	}
	h.admitted(res.Room, p, res.Existing)
	return false
}

// admitted tells the joiner it is in and tells the right existing peers about it.
func (h *Handler) admitted(r *room.Room, p *room.Peer, existing []*room.Peer) {
	h.Metrics.Join()
	h.log().Debug("peer joined", "bucket", r.Bucket)
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
		h.log().Info("room closed", "bucket", res.Room.Bucket, "reason", "host_left")
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
	h.log().Info("bucket rooms closed", "bucket", id, "reason", reason)
}

// CloseRoom closes one room; it reports whether the room existed.
func (h *Handler) CloseRoom(bucketID, code, reason string) bool {
	ps, ok := h.Rooms.CloseRoom(bucketID, code)
	for _, p := range ps {
		p.Send(roomClosed{"room_closed", reason})
	}
	if ok {
		h.log().Info("room closed", "bucket", bucketID, "reason", reason)
	}
	return ok
}
