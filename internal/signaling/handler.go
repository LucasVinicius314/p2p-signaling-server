package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/metrics"
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
)

// Handler serves GET /ws.
type Handler struct {
	Buckets *bucket.Service
	Rooms   *room.Manager
	Log     *slog.Logger
	Metrics *metrics.Metrics // optional
}

type conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	name   string
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
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin is checked per bucket after hello
	})
	if err != nil {
		return
	}
	h.Metrics.Connection()
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, outQueue)}
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

	p := &room.Peer{Send: c.send}
	defer h.leave(p)
	h.readLoop(c, p, b)
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
		T: "welcome", IceServers: b.IceServers, IcePolicy: policy, Protocol: ProtocolVersion,
		Limits: welcomeLims{b.Limits.MessageBytes, b.Limits.MessagesPerSecond, b.MaxPlayers},
	})
	wctx, wcancel := context.WithTimeout(c.ctx, writeTimeout)
	defer wcancel()
	if err := c.ws.Write(wctx, websocket.MessageText, data); err != nil {
		return bucket.Bucket{}, false
	}
	name := hi.Name
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	c.name = name
	return b, true
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
		case *signalIn:
			h.signal(c, p, m, b.Limits.MessageBytes)
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
	max := b.MaxPlayers
	if m.MaxPlayers != 0 {
		if m.MaxPlayers < 2 || m.MaxPlayers > b.MaxPlayers {
			c.send(newError(ErrBadMessage, "max_players out of range"))
			return
		}
		max = m.MaxPlayers
	}
	p.Name = c.name
	r, err := h.Rooms.Create(b.ID, room.Params{
		Topology: b.Topology, MaxPlayers: max, MaxRooms: b.MaxRooms, CodeLength: b.CodeLength,
	}, p)
	switch {
	case errors.Is(err, room.ErrBusy):
		c.send(newError(ErrServerBusy, "no room capacity"))
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
	default:
		h.Metrics.RoomCreated()
		c.send(roomCreated{"room_created", r.Code, p.ID})
	}
}

func (h *Handler) join(c *conn, p *room.Peer, b bucket.Bucket, m *joinRoom) {
	code := strings.ToUpper(strings.TrimSpace(m.Code))
	p.Name = c.name
	r, existing, err := h.Rooms.Join(b.ID, code, p)
	switch {
	case errors.Is(err, room.ErrNotFound):
		h.Metrics.FailedJoin()
		c.send(newError(ErrRoomNotFound, "room not found"))
		return
	case errors.Is(err, room.ErrFull):
		c.send(newError(ErrRoomFull, "room is full"))
		return
	case err != nil:
		c.send(newError(ErrBadMessage, err.Error()))
		return
	}
	ids := []int32{room.HostID}
	if r.Topology == "mesh" {
		ids = ids[:0]
		for _, o := range existing {
			ids = append(ids, o.ID)
		}
	}
	h.Metrics.Join()
	p.Send(joined{"joined", p.ID, room.HostID, ids})
	for _, o := range existing {
		if r.Topology == "mesh" || o == r.Host() {
			o.Send(peerEvent{"peer_joined", p.ID})
		}
	}
}

func (h *Handler) signal(c *conn, p *room.Peer, m *signalIn, maxBytes int) {
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
	if res.Closed {
		for _, o := range res.Others {
			o.Send(roomClosed{"room_closed", "host_left"})
		}
		return
	}
	for _, o := range res.Others {
		if res.Room.Topology == "mesh" || o == res.Room.Host() {
			o.Send(peerEvent{"peer_left", res.Left})
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
