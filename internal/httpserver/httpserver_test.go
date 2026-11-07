package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func status(h http.Handler, path string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code
}

func TestHealthAndReady(t *testing.T) {
	ready := false
	h := New(func() bool { return ready })
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
