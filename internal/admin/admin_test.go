package admin_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"p2p-signaling-server/internal/admin"
	"p2p-signaling-server/internal/auth"
	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/store/memory"
)

const key = "0123456789abcdef0123456789abcdef"

func server(t *testing.T) *httptest.Server {
	t.Helper()
	svc := bucket.NewService(memory.New())
	if err := svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	admin.Handler{Buckets: svc}.Register(mux, auth.Middleware(key, nil))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func do(t *testing.T, s *httptest.Server, method, path, body, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := res.Body.Read(b)
		sb.Write(b[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, sb.String()
}

func TestAuthRequired(t *testing.T) {
	s := server(t)
	for _, tok := range []string{"", "wrong"} {
		if c, _ := do(t, s, "GET", "/admin/v1/buckets", "", tok); c != 401 {
			t.Fatalf("token %q: got %d", tok, c)
		}
	}
}

func TestCRUDAndAudit(t *testing.T) {
	s := server(t)
	if c, b := do(t, s, "POST", "/admin/v1/buckets", `{"id":"racer","name":"Racer","max_players":4,"accepted_versions":["1.0.0"]}`, key); c != 201 {
		t.Fatalf("create %d %s", c, b)
	}
	if c, _ := do(t, s, "POST", "/admin/v1/buckets", `{"id":"racer","name":"Racer"}`, key); c != 409 {
		t.Fatalf("duplicate %d", c)
	}
	if c, _ := do(t, s, "POST", "/admin/v1/buckets", `{"id":"Bad_ID","name":"x"}`, key); c != 400 {
		t.Fatalf("bad id %d", c)
	}
	if c, _ := do(t, s, "PATCH", "/admin/v1/buckets/racer", `{"relay_only":true}`, key); c != 400 {
		t.Fatalf("relay_only without turn %d", c)
	}
	if c, _ := do(t, s, "PATCH", "/admin/v1/buckets/racer", `{"bogus":1}`, key); c != 400 {
		t.Fatalf("unknown field %d", c)
	}
	if c, b := do(t, s, "PATCH", "/admin/v1/buckets/racer", `{"enabled":false}`, key); c != 200 || !strings.Contains(b, `"enabled":false`) {
		t.Fatalf("patch %d %s", c, b)
	}
	if c, _ := do(t, s, "GET", "/admin/v1/buckets/nope", "", key); c != 404 {
		t.Fatalf("missing %d", c)
	}
	if c, b := do(t, s, "GET", "/admin/v1/audit", "", key); c != 200 || !strings.Contains(b, "update_bucket") || !strings.Contains(b, `"rest"`) {
		t.Fatalf("audit %d %s", c, b)
	}
	if c, _ := do(t, s, "DELETE", "/admin/v1/buckets/racer", "", key); c != 204 {
		t.Fatalf("delete %d", c)
	}
}
