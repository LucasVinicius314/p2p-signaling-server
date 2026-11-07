package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"p2p-signaling-server/internal/auth"
	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/mcpserver"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/ops"
	"p2p-signaling-server/internal/room"
	"p2p-signaling-server/internal/store/memory"
)

type noCloser struct{}

func (noCloser) CloseRoom(string, string, string) bool { return false }
func (noCloser) CloseBucket(string, string)            {}

const key = "0123456789abcdef0123456789abcdef"

func newOps(t *testing.T) *ops.Ops {
	t.Helper()
	svc := bucket.NewService(memory.New())
	if err := svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &ops.Ops{Buckets: svc, Rooms: room.NewManager(), Closer: noCloser{}, Metrics: metrics.New(), Version: "test"}
}

func session(t *testing.T, o *ops.Ops) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(o).Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		return nil, true
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: bad structured content %s", tool, b)
	}
	return m, false
}

func TestToolsListed(t *testing.T) {
	cs := session(t, newOps(t))
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"list_buckets", "get_bucket", "create_bucket", "update_bucket", "delete_bucket",
		"set_bucket_enabled", "list_rooms", "close_room", "get_stats", "get_health", "get_audit_log", "validate_bucket"}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("missing tool %s", n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d tools, want %d", len(got), len(want))
	}
}

func TestBucketFlow(t *testing.T) {
	cs := session(t, newOps(t))

	if _, isErr := call(t, cs, "create_bucket", map[string]any{"id": "racer"}); !isErr {
		t.Fatal("create without name should fail")
	}
	out, isErr := call(t, cs, "create_bucket", map[string]any{
		"id": "racer", "name": "Racer", "max_players": 4, "accepted_versions": []string{"1.0.0", "1.0.1"},
		"limits": map[string]any{"rooms_per_ip": 3},
	})
	if isErr {
		t.Fatal("create failed")
	}
	b := out["bucket"].(map[string]any)
	if b["max_players"] != float64(4) || b["topology"] != "star" {
		t.Fatalf("created: %v", b)
	}
	lim := b["limits"].(map[string]any)
	if lim["rooms_per_ip"] != float64(3) || lim["message_bytes"] != float64(16384) {
		t.Fatalf("partial limits must keep defaults: %v", lim)
	}

	if _, isErr := call(t, cs, "create_bucket", map[string]any{"id": "racer", "name": "again"}); !isErr {
		t.Fatal("duplicate should fail")
	}
	if _, isErr := call(t, cs, "update_bucket", map[string]any{"id": "racer", "relay_only": true}); !isErr {
		t.Fatal("relay_only without turn should fail")
	}
	v, _ := call(t, cs, "validate_bucket", map[string]any{"id": "racer", "max_players": 99})
	if v["valid"] == true || v["error"] == nil {
		t.Fatalf("validate should report an error: %v", v)
	}
	if v, _ := call(t, cs, "validate_bucket", map[string]any{"id": "newgame", "name": "New"}); v["valid"] != true {
		t.Fatalf("validate new: %v", v)
	}

	if out, _ := call(t, cs, "set_bucket_enabled", map[string]any{"id": "racer", "enabled": false}); out["bucket"].(map[string]any)["enabled"] != false {
		t.Fatalf("disable: %v", out)
	}
	l, _ := call(t, cs, "list_buckets", nil)
	if bs := l["buckets"].([]any); len(bs) != 1 || bs[0].(map[string]any)["enabled"] != false {
		t.Fatalf("list: %v", l)
	}

	if _, isErr := call(t, cs, "delete_bucket", map[string]any{"id": "racer", "confirm": "wrong"}); !isErr {
		t.Fatal("delete without matching confirm should fail")
	}
	if _, isErr := call(t, cs, "delete_bucket", map[string]any{"id": "racer", "confirm": "racer"}); isErr {
		t.Fatal("delete failed")
	}
	if _, isErr := call(t, cs, "get_bucket", map[string]any{"id": "racer"}); !isErr {
		t.Fatal("deleted bucket should be gone")
	}

	a, _ := call(t, cs, "get_audit_log", map[string]any{"limit": 10})
	var actions []string
	for _, e := range a["entries"].([]any) {
		m := e.(map[string]any)
		if m["actor"] != "mcp" {
			t.Fatalf("audit actor: %v", m)
		}
		actions = append(actions, m["action"].(string))
	}
	if strings.Join(actions, ",") != "delete_bucket,update_bucket,create_bucket" {
		t.Fatalf("audit actions (newest first): %v", actions)
	}
}

func TestReadOnlyTools(t *testing.T) {
	cs := session(t, newOps(t))
	if h, _ := call(t, cs, "get_health", nil); h["health"].(map[string]any)["status"] != "ok" {
		t.Fatalf("health: %v", h)
	}
	if s, _ := call(t, cs, "get_stats", nil); s["stats"].(map[string]any)["rooms_active"] != float64(0) {
		t.Fatalf("stats: %v", s)
	}
	if r, _ := call(t, cs, "list_rooms", nil); len(r["rooms"].([]any)) != 0 {
		t.Fatalf("rooms: %v", r)
	}
	if _, isErr := call(t, cs, "close_room", map[string]any{"bucket": "x", "code": "ABCDE"}); !isErr {
		t.Fatal("closing a missing room should fail")
	}
}

func TestHTTPEndpointRequiresKey(t *testing.T) {
	h := auth.Middleware(key, nil)(mcpserver.Handler(mcpserver.New(newOps(t))))
	srv := httptest.NewServer(h)
	defer srv.Close()

	post := func(token string) int {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", srv.URL, strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := post(""); c != 401 {
		t.Fatalf("no key: %d", c)
	}
	if c := post(key); c != 200 {
		t.Fatalf("with key: %d", c)
	}
}
