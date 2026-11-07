// Package httpserver builds the HTTP router and middleware.
package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// Ready reports whether the first config load has completed.
type Ready func() bool

// New builds the router. log may be nil (nothing is logged). Every response carries an X-Request-Id; panics are
// logged with a stack trace and answered with a 500; requests other than the WebSocket upgrade are logged
// with their route pattern, status and duration (never the path itself, which can contain room codes).
func New(log *slog.Logger, ready Ready, register ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	for _, r := range register {
		r(mux)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if ready == nil || !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	return requestScope(log, mux)
}

// statusWriter records the status code. It unwraps for http.ResponseController (flush, hijack).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestScope(log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)

		// The WebSocket upgrade takes over the connection; it is not wrapped or access-logged here
		// (the signaling handler logs its own events).
		if r.URL.Path == "/ws" {
			defer recoverPanic(log, w, id)
			next.ServeHTTP(w, r)
			return
		}

		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			route := routeOf(r)
			level := slog.LevelInfo
			if route == "GET /health" || route == "GET /ready" {
				level = slog.LevelDebug // health checks run every few seconds
			}
			log.Log(r.Context(), level, "request", "request_id", id, "method", r.Method, "route", route,
				"status", status, "duration_ms", time.Since(start).Milliseconds())
		}()
		defer recoverPanic(log, sw, id)
		next.ServeHTTP(sw, r)
	})
}

func routeOf(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

func recoverPanic(log *slog.Logger, w http.ResponseWriter, id string) {
	if rec := recover(); rec != nil {
		log.Error("panic in handler", "request_id", id, "panic", rec, "stack", string(debug.Stack()))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
