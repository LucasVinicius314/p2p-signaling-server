package e2e

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"p2p-signaling-server/internal/signaling"
)

// byTestHeader makes the client address come from the X-Test-IP header, standing in for a trusted proxy.
func byTestHeader(h *signaling.Handler) {
	h.ClientIP = func(r *http.Request) netip.Addr { return netip.MustParseAddr(r.Header.Get("X-Test-IP")) }
}

// asHello connects from the given address and completes the handshake.
func (e *env) asHello(t *testing.T, ip string) *client {
	t.Helper()
	c := e.dialAs(t, ip)
	c.hello("1.0.0")
	c.expect("welcome")
	return c
}

// closeStatus reads until the server closes the socket and returns the close code (-1 if it was not a close).
func closeStatus(t *testing.T, c *client) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := c.ws.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestDefaultCodesAreSixCharacters(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	_, code := e.hostWithCode(t)
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
}

func TestIPv6AddressesInOneSlash64ShareFailedJoinLockout(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","limits":{"failed_joins_before_lockout":2,"failed_joins_per_connection":50,"joins_per_ip_per_minute":100}}`, byTestHeader)
	host := e.asHello(t, "198.51.100.1")
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]

	// Rotating addresses inside one /64 must not reset the guesser's count.
	a := e.asHello(t, "2001:db8:1:2::1")
	for i := 0; i < 2; i++ {
		a.send(msg{"t": "join_room", "code": "ZZZZZZ"})
		a.expectErr("room_not_found")
	}
	b := e.asHello(t, "2001:db8:1:2:aaaa:bbbb:cccc:9")
	b.send(msg{"t": "join_room", "code": code})
	b.expectErr("locked_out")

	other := e.asHello(t, "2001:db8:1:3::1") // a different /64 is a different subscriber
	other.send(msg{"t": "join_room", "code": code})
	other.expect("joined")
}

func TestTooManyWrongCodesDropsThatConnectionOnly(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","limits":{"failed_joins_per_connection":3,"failed_joins_before_lockout":100,"joins_per_ip_per_minute":100}}`, nil)
	_, code := e.hostWithCode(t)

	guesser := e.connected(t)
	for i := 0; i < 3; i++ {
		guesser.send(msg{"t": "join_room", "code": "ZZZZZZ"})
		guesser.expectErr("room_not_found") // the third answer is delivered before the drop
	}
	if st := closeStatus(t, guesser); st != websocket.StatusPolicyViolation {
		t.Fatalf("want the connection dropped with a policy violation, got %v", st)
	}

	// Others behind the same address are not locked out.
	friend := e.connected(t)
	friend.send(msg{"t": "join_room", "code": code})
	friend.expect("joined")
}

func TestBucketWideFailedJoinBudgetStopsDistributedGuessing(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game","limits":{"failed_joins_per_minute":3,"failed_joins_per_connection":50,"failed_joins_before_lockout":500,"joins_per_ip_per_minute":500}}`, byTestHeader)
	host := e.asHello(t, "198.51.100.1")
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]

	// Three wrong codes from three unrelated addresses spend the bucket's budget...
	for i, ip := range []string{"203.0.113.1", "203.0.113.2", "2001:db8:9:9::1"} {
		g := e.asHello(t, ip)
		g.send(msg{"t": "join_room", "code": "ZZZZZZ"})
		g.expectErr("room_not_found")
		_ = i
	}
	// ...after which joins are refused for everyone until it refills, even with the right code.
	late := e.asHello(t, "192.0.2.77")
	late.send(msg{"t": "join_room", "code": code})
	late.expectErr("rate_limited")
}

func TestOpenConnectionsAreCappedPerAddress(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game"}`, func(h *signaling.Handler) { h.MaxConnsPerIP = 2 })
	first := e.connected(t)
	_ = e.connected(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", nil)
	if err == nil {
		t.Fatal("a third open connection from one address must be refused")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %+v (%v)", resp, err)
	}

	// Closing one frees its slot.
	first.ws.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", nil)
		if err == nil {
			c.CloseNow()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot was never freed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestShutdownTellsClientsToRetryAndRefusesNewOnes(t *testing.T) {
	e, _ := setupWith(t, `{"name":"Game"}`, nil)
	host, _ := e.hostWithCode(t)
	joiner := e.connected(t) // connected but not in a room

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		e.sig.Shutdown(ctx, 3*time.Second)
		close(done)
	}()

	for _, c := range []*client{host, joiner} {
		m := c.expect("server_restarting")
		if id(m, "retry_after_ms") != 3000 {
			t.Fatalf("retry hint: %v", m)
		}
		if st := closeStatus(t, c); st != websocket.StatusServiceRestart {
			t.Fatalf("want close code 1012 (service restart), got %v", st)
		}
	}
	<-done

	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a draining server must answer 503, got %+v (%v)", resp, err)
	}
}

func TestCreateRoomNoLongerAcceptsMeta(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	c := e.connected(t)
	c.send(msg{"t": "create_room", "meta": msg{"mode": "ranked"}})
	c.expectErr("bad_message")
}
