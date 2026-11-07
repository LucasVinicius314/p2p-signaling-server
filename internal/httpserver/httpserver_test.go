package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func status(h http.Handler, path string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code
}

func TestHealthAndReady(t *testing.T) {
	ready := false
	h := New(nil, func() bool { return ready })
	if c := status(h, "/health"); c != 200 {
		t.Fatalf("health %d", c)
	}
	if c := status(h, "/ready"); c != 503 {
		t.Fatalf("ready before ready %d", c)
	}
	ready = true
	if c := status(h, "/ready"); c != 200 {
		t.Fatalf("ready after ready %d", c)
	}
}

func TestPanicIsLoggedWithAStackAndAnswered500(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := New(log, nil, func(m *http.ServeMux) {
		m.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "kaboom") || !strings.Contains(out, "stack") {
		t.Fatalf("panic was not logged with a stack: %s", out)
	}
	if rec.Header().Get("X-Request-Id") == "" || !strings.Contains(out, rec.Header().Get("X-Request-Id")) {
		t.Fatalf("the log line must carry the response's request id: %s", out)
	}
}

func TestAccessLogUsesTheRouteNotThePath(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := New(log, nil, func(m *http.ServeMux) {
		m.HandleFunc("POST /admin/v1/rooms/{bucket}/{code}/close", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(204)
		})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/v1/rooms/game/K7M2QX/close", nil))
	out := buf.String()
	if strings.Contains(out, "K7M2QX") {
		t.Fatalf("a join code leaked into the access log: %s", out)
	}
	if !strings.Contains(out, "/admin/v1/rooms/{bucket}/{code}/close") || !strings.Contains(out, `"status":204`) {
		t.Fatalf("route and status expected in the log: %s", out)
	}
}

func TestHealthChecksAreNotLoggedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := New(log, nil)
	status(h, "/health")
	if buf.Len() != 0 {
		t.Fatalf("health checks must stay out of the info log: %s", buf.String())
	}
}
