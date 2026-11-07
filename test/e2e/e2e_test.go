package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/room"
	"p2p-signaling-server/internal/signaling"
	"p2p-signaling-server/internal/store/memory"
)

type env struct {
	srv *httptest.Server
	svc *bucket.Service
	sig *signaling.Handler
}

func setup(t *testing.T, overlay string) *env {
	t.Helper()
	svc := bucket.NewService(memory.New())
	if _, err := svc.Create(t.Context(), "test", "game", json.RawMessage(overlay)); err != nil {
		t.Fatal(err)
	}
	sig := &signaling.Handler{Buckets: svc, Rooms: room.NewManager()}
	mux := http.NewServeMux()
	mux.Handle("GET /ws", sig)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{srv, svc, sig}
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
}

func (e *env) dial(t *testing.T, header http.Header) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &client{t, ws}
}

func (c *client) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	if err := c.ws.Write(c.t.Context(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

type msg map[string]any

func (c *client) recv() msg {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	_, raw, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var m msg
	if err := json.Unmarshal(raw, &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func (c *client) expect(typ string) msg {
	c.t.Helper()
	m := c.recv()
	if m["t"] != typ {
		c.t.Fatalf("want %q, got %v", typ, m)
	}
	return m
}

func (c *client) expectErr(code string) {
	c.t.Helper()
	if m := c.expect("error"); m["code"] != code {
		c.t.Fatalf("want error %q, got %v", code, m)
	}
}

func (c *client) hello(version string) {
	c.send(msg{"t": "hello", "bucket": "game", "game_version": version, "protocol": 1})
}

func (e *env) connected(t *testing.T) *client {
	c := e.dial(t, nil)
	c.hello("1.0.0")
	c.expect("welcome")
	return c
}

func id(m msg, k string) int { return int(m[k].(float64)) }

func TestFullSequence(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	rc := host.expect("room_created")
	code := rc["code"].(string)
	if id(rc, "peer_id") != 1 || len(code) != 5 {
		t.Fatalf("room_created: %v", rc)
	}

	cl := e.connected(t)
	cl.send(msg{"t": "join_room", "code": strings.ToLower(code)}) // case-insensitive
	j := cl.expect("joined")
	cid := id(j, "peer_id")
	if cid < 2 || id(j, "host_id") != 1 || len(j["peers"].([]any)) != 1 {
		t.Fatalf("joined: %v", j)
	}
	if pj := host.expect("peer_joined"); id(pj, "peer_id") != cid {
		t.Fatalf("peer_joined: %v", pj)
	}

	// client offers, host answers, candidates flow
	cl.send(msg{"t": "signal", "to": 1, "kind": "offer", "data": msg{"sdp": "x"}})
	s := host.expect("signal")
	if id(s, "from") != cid || s["kind"] != "offer" || s["data"].(map[string]any)["sdp"] != "x" {
		t.Fatalf("relayed offer: %v", s)
	}
	host.send(msg{"t": "signal", "to": cid, "kind": "answer", "data": msg{"sdp": "y"}})
	if s := cl.expect("signal"); s["kind"] != "answer" || id(s, "from") != 1 {
		t.Fatalf("relayed answer: %v", s)
	}
	cl.send(msg{"t": "signal", "to": 1, "kind": "candidate", "data": msg{"c": 1}})
	host.expect("signal")

	cl.send(msg{"t": "leave"})
	if pl := host.expect("peer_left"); id(pl, "peer_id") != cid {
		t.Fatalf("peer_left: %v", pl)
	}
	host.send(msg{"t": "ping"})
	host.expect("pong")
}

func TestHostLeavingClosesRoom(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]
	cl := e.connected(t)
	cl.send(msg{"t": "join_room", "code": code})
	cl.expect("joined")
	host.expect("peer_joined")

	host.ws.Close(websocket.StatusNormalClosure, "")
	if m := cl.expect("room_closed"); m["reason"] != "host_left" {
		t.Fatalf("got %v", m)
	}
	late := e.connected(t)
	late.send(msg{"t": "join_room", "code": code})
	late.expectErr("room_not_found")
}

func TestWrongCodeLooksLikeMissingRoom(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	host.expect("room_created")
	c := e.connected(t)
	var errs []msg
	for _, code := range []string{"AAAAA", "x", "", "waytoolongcodehere"} {
		c.send(msg{"t": "join_room", "code": code})
		errs = append(errs, c.expect("error"))
	}
	for _, m := range errs {
		if m["code"] != "room_not_found" || m["message"] != errs[0]["message"] {
			t.Fatalf("errors differ: %v", errs)
		}
	}
}

func TestStarForbidsClientToClient(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]
	a, b := e.connected(t), e.connected(t)
	a.send(msg{"t": "join_room", "code": code})
	aid := id(a.expect("joined"), "peer_id")
	b.send(msg{"t": "join_room", "code": code})
	b.expect("joined")
	b.send(msg{"t": "signal", "to": aid, "kind": "offer", "data": 1})
	b.expectErr("bad_message")
}

func TestMeshAllowsClientToClient(t *testing.T) {
	e := setup(t, `{"name":"Game","topology":"mesh"}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]
	a, b := e.connected(t), e.connected(t)
	a.send(msg{"t": "join_room", "code": code})
	aid := id(a.expect("joined"), "peer_id")
	host.expect("peer_joined")
	b.send(msg{"t": "join_room", "code": code})
	j := b.expect("joined")
	if len(j["peers"].([]any)) != 2 {
		t.Fatalf("mesh joiner should learn all peers: %v", j)
	}
	a.expect("peer_joined") // mesh: existing peers are told too
	host.expect("peer_joined")
	b.send(msg{"t": "signal", "to": aid, "kind": "offer", "data": 1})
	a.expect("signal")
}

func TestRoomFull(t *testing.T) {
	e := setup(t, `{"name":"Game","max_players":2}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	code := host.expect("room_created")["code"]
	a, b := e.connected(t), e.connected(t)
	a.send(msg{"t": "join_room", "code": code})
	a.expect("joined")
	b.send(msg{"t": "join_room", "code": code})
	b.expectErr("room_full")
}

func TestHelloRejections(t *testing.T) {
	e := setup(t, `{"name":"Game","accepted_versions":["1.0.0"],"allowed_origins":["https://ok.example"]}`)
	cases := []struct {
		name   string
		header http.Header
		hello  msg
		code   string
	}{
		{"version", nil, msg{"t": "hello", "bucket": "game", "game_version": "9", "protocol": 1}, "version_rejected"},
		{"bucket", nil, msg{"t": "hello", "bucket": "nope", "game_version": "1.0.0", "protocol": 1}, "bucket_not_found"},
		{"protocol", nil, msg{"t": "hello", "bucket": "game", "game_version": "1.0.0", "protocol": 7}, "unsupported_protocol"},
		{"origin", http.Header{"Origin": {"https://evil.example"}}, msg{"t": "hello", "bucket": "game", "game_version": "1.0.0", "protocol": 1}, "origin_rejected"},
		{"not hello", nil, msg{"t": "create_room"}, "bad_message"},
		{"unknown field", nil, msg{"t": "hello", "bucket": "game", "game_version": "1.0.0", "protocol": 1, "extra": 1}, "bad_message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := e.dial(t, tc.header)
			c.send(tc.hello)
			c.expectErr(tc.code)
		})
	}
	c := e.dial(t, http.Header{"Origin": {"https://ok.example"}})
	c.hello("1.0.0")
	c.expect("welcome")
}

func TestDisabledBucket(t *testing.T) {
	e := setup(t, `{"name":"Game","enabled":false}`)
	c := e.dial(t, nil)
	c.hello("1")
	c.expectErr("bucket_disabled")
}

func TestBadMessagesAreRejectedNotFatal(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	c := e.connected(t)
	for _, raw := range []string{`{"t":"nope"}`, `not json`, `{"t":"join_room","code":"A","x":1}`, `{"t":"signal","to":1,"kind":"weird","data":1}`} {
		if err := c.ws.Write(t.Context(), websocket.MessageText, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		c.expectErr("bad_message")
	}
	c.send(msg{"t": "ping"})
	c.expect("pong")
}

func TestOversizedMessageDropsConnection(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	c := e.connected(t)
	big := `{"t":"signal","to":1,"kind":"offer","data":"` + strings.Repeat("a", 20000) + `"}`
	_ = c.ws.Write(t.Context(), websocket.MessageText, []byte(big))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, _, err := c.ws.Read(ctx); err == nil {
		t.Fatal("expected connection to close")
	}
}

func TestMaxRoomsServerBusy(t *testing.T) {
	e := setup(t, `{"name":"Game","max_rooms":1}`)
	a, b := e.connected(t), e.connected(t)
	a.send(msg{"t": "create_room"})
	a.expect("room_created")
	b.send(msg{"t": "create_room"})
	b.expectErr("server_busy")
}

func TestCloseBucketNotifiesPeers(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	host := e.connected(t)
	host.send(msg{"t": "create_room"})
	host.expect("room_created")
	e.sig.CloseBucket("game", "bucket_deleted")
	if m := host.expect("room_closed"); m["reason"] != "bucket_deleted" {
		t.Fatalf("got %v", m)
	}
}
