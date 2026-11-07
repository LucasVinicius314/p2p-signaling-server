package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/room"
	"p2p-signaling-server/internal/signaling"
	"p2p-signaling-server/internal/store/memory"
)

// setupWith is setup with access to the handler (before it serves) and its metrics.
func setupWith(t *testing.T, overlay string, tweak func(*signaling.Handler)) (*env, *metrics.Metrics) {
	t.Helper()
	svc := bucket.NewService(memory.New())
	if _, err := svc.Create(t.Context(), "test", "game", json.RawMessage(overlay)); err != nil {
		t.Fatal(err)
	}
	met := metrics.New()
	sig := &signaling.Handler{Buckets: svc, Rooms: room.NewManager(), Metrics: met}
	if tweak != nil {
		tweak(sig)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /ws", sig)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{srv, svc, sig}, met
}

// namedClient connects with a player name.
func (e *env) namedClient(t *testing.T, name string) *client {
	t.Helper()
	c := e.dial(t, nil)
	c.send(msg{"t": "hello", "bucket": "game", "game_version": "1.0.0", "protocol": 1, "name": name})
	c.expect("welcome")
	return c
}

func (e *env) hostWithCode(t *testing.T) (*client, string) {
	t.Helper()
	h := e.connected(t)
	h.send(msg{"t": "create_room"})
	return h, h.expect("room_created")["code"].(string)
}

func TestApprovalFlow(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","host_approval":true}`, nil)
	host, code := e.hostWithCode(t)

	cl := e.namedClient(t, "A\u0007lice") // control character must be stripped
	cl.send(msg{"t": "join_room", "code": code})
	cl.expect("join_pending")
	req := host.expect("join_request")
	if req["name"] != "Alice" {
		t.Fatalf("name should be sanitized, got %q", req["name"])
	}
	if _, leaks := req["ip"]; leaks || len(req) != 3 {
		t.Fatalf("join_request must carry only t, peer and name: %v", req)
	}
	pid := id(req, "peer")

	// a pending joiner cannot signal, and cannot approve itself
	cl.send(msg{"t": "signal", "to": 1, "kind": "offer", "data": 1})
	cl.expectErr("bad_message")
	cl.send(msg{"t": "approve", "peer": pid, "accept": true})
	cl.expectErr("not_host")

	host.send(msg{"t": "approve", "peer": pid, "accept": true})
	j := cl.expect("joined")
	if id(j, "peer_id") != pid || id(j, "host_id") != 1 {
		t.Fatalf("joined: %v", j)
	}
	host.expect("peer_joined")
	cl.send(msg{"t": "signal", "to": 1, "kind": "offer", "data": 1})
	host.expect("signal")
}

func TestApprovalDenied(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","host_approval":true}`, nil)
	host, code := e.hostWithCode(t)
	cl := e.connected(t)
	cl.send(msg{"t": "join_room", "code": code})
	cl.expect("join_pending")
	pid := id(host.expect("join_request"), "peer")
	host.send(msg{"t": "approve", "peer": pid, "accept": false})
	cl.expectErr("join_denied")
	host.send(msg{"t": "approve", "peer": pid}) // missing accept
	host.expectErr("bad_message")
	host.send(msg{"t": "approve", "peer": pid, "accept": true}) // already decided
	host.expectErr("bad_message")
}

func TestPendingJoinerLeavesIsCancelled(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","host_approval":true}`, nil)
	host, code := e.hostWithCode(t)
	cl := e.connected(t)
	cl.send(msg{"t": "join_room", "code": code})
	cl.expect("join_pending")
	pid := id(host.expect("join_request"), "peer")
	cl.ws.CloseNow()
	if m := host.expect("join_cancelled"); id(m, "peer") != pid {
		t.Fatalf("got %v", m)
	}
}

func TestPendingJoinTimesOut(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","host_approval":true}`, func(h *signaling.Handler) {
		h.PendingTTL = 150 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.sig.Run(ctx, 30*time.Millisecond)

	host, code := e.hostWithCode(t)
	cl := e.connected(t)
	cl.send(msg{"t": "join_room", "code": code})
	cl.expect("join_pending")
	pid := id(host.expect("join_request"), "peer")
	cl.expectErr("join_denied")
	if m := host.expect("join_cancelled"); id(m, "peer") != pid {
		t.Fatalf("got %v", m)
	}
}

func TestLockRoom(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game"}`, nil)
	host, code := e.hostWithCode(t)
	a, b := e.connected(t), e.connected(t)

	a.send(msg{"t": "join_room", "code": code})
	a.expect("joined")
	host.expect("peer_joined")

	a.send(msg{"t": "lock_room", "locked": true})
	a.expectErr("not_host")
	host.send(msg{"t": "lock_room"})
	host.expectErr("bad_message")

	host.send(msg{"t": "lock_room", "locked": true})
	host.sync() // lock_room has no reply; a ping/pong guarantees it was applied
	b.send(msg{"t": "join_room", "code": code})
	b.expectErr("room_locked")

	host.send(msg{"t": "lock_room", "locked": false})
	host.sync()
	b.send(msg{"t": "join_room", "code": code})
	b.expect("joined")
}

// sync waits until the server has processed everything this client sent so far
// (messages on one connection are handled in order).
func (c *client) sync() {
	c.t.Helper()
	c.send(msg{"t": "ping"})
	c.expect("pong")
}

func TestLockoutAfterFailedJoins(t *testing.T) {
	e, met := setupWith(t, `{"name":"Game","limits":{"failed_joins_before_lockout":3,"joins_per_ip_per_minute":100}}`, nil)
	_, code := e.hostWithCode(t)
	guesser := e.connected(t)
	for i := 0; i < 3; i++ {
		guesser.send(msg{"t": "join_room", "code": "AAAA" + string(rune('A'+i))})
		guesser.expectErr("room_not_found")
	}
	// now locked out: even the correct code is refused, and says so
	guesser.send(msg{"t": "join_room", "code": code})
	guesser.expectErr("locked_out")

	// the lockout is per address, so a second connection from the same address is blocked too
	other := e.connected(t)
	other.send(msg{"t": "join_room", "code": code})
	other.expectErr("locked_out")

	s := met.Snapshot(nil)
	if s.Lockouts != 1 || s.FailedJoins != 3 || s.RateLimitHits < 2 {
		t.Fatalf("metrics: %+v", s)
	}
}

func TestJoinRateLimit(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","limits":{"joins_per_ip_per_minute":3}}`, nil)
	c := e.connected(t)
	for i := 0; i < 3; i++ {
		c.send(msg{"t": "join_room", "code": "AAAAA"})
		c.expectErr("room_not_found")
	}
	c.send(msg{"t": "join_room", "code": "AAAAA"})
	c.expectErr("rate_limited")
}

func TestRoomsPerIP(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","limits":{"rooms_per_ip":1}}`, nil)
	a, b := e.connected(t), e.connected(t)
	a.send(msg{"t": "create_room"})
	a.expect("room_created")
	b.send(msg{"t": "create_room"})
	b.expectErr("rate_limited")

	// freeing the room frees the quota
	a.send(msg{"t": "leave"})
	a.sync()
	b.send(msg{"t": "create_room"})
	b.expect("room_created")
}

func TestLockoutIsPerAddress(t *testing.T) {
	// the "client address" comes from a test header, standing in for a trusted proxy
	e, _ := setupWith(t, `{"name":"Game","limits":{"failed_joins_before_lockout":2,"joins_per_ip_per_minute":100}}`, func(h *signaling.Handler) {
		h.ClientIP = func(r *http.Request) netip.Addr { return netip.MustParseAddr(r.Header.Get("X-Test-IP")) }
	})
	host := e.dialAs(t, "198.51.100.1")
	host.hello("1.0.0")
	host.expect("welcome")
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]

	bad := e.dialAs(t, "203.0.113.5")
	bad.hello("1.0.0")
	bad.expect("welcome")
	for i := 0; i < 2; i++ {
		bad.send(msg{"t": "join_room", "code": "ZZZZZ"})
		bad.expectErr("room_not_found")
	}
	bad.send(msg{"t": "join_room", "code": code})
	bad.expectErr("locked_out")

	good := e.dialAs(t, "203.0.113.6")
	good.hello("1.0.0")
	good.expect("welcome")
	good.send(msg{"t": "join_room", "code": code})
	good.expect("joined")
}

func (e *env) dialAs(t *testing.T, ip string) *client {
	return e.dial(t, http.Header{"X-Test-IP": {ip}})
}
