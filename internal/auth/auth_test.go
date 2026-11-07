package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func status(h http.Handler, token string) int {
	req := httptest.NewRequest("GET", "/", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestMiddleware(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	h := Middleware(key, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if c := status(h, key); c != 200 {
		t.Fatalf("right key: %d", c)
	}
	for _, tok := range []string{"", "wrong", key + "x", key[:10]} {
		if c := status(h, tok); c != 401 {
			t.Fatalf("token %q: %d", tok, c)
		}
	}
	// scheme must be Bearer
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("raw key without Bearer: %d", rec.Code)
	}
}

func TestFailedAttemptsAreLimitedButTheRightKeyAlwaysWorks(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	fails := 0
	h := Middleware(key, func(*http.Request) bool { fails++; return fails <= 2 })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if c := status(h, "a"); c != 401 {
		t.Fatalf("1st failure: %d", c)
	}
	if c := status(h, "b"); c != 401 {
		t.Fatalf("2nd failure: %d", c)
	}
	if c := status(h, "c"); c != 429 {
		t.Fatalf("3rd failure should be rate limited: %d", c)
	}
	if c := status(h, key); c != 200 {
		t.Fatalf("the owner must never be locked out: %d", c)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	h := Middleware(key, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	for _, header := range []string{"Bearer " + key, "bearer " + key, "BEARER " + key, "  Bearer   " + key + "  "} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%q: %d", header, rec.Code)
		}
	}
	for _, header := range []string{"Basic " + key, "Bearer", "Bearer ", "Bearerx " + key} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%q must be refused, got %d", header, rec.Code)
		}
	}
}
