// Package httpserver builds the HTTP router and middleware.
package httpserver

import (
	"net/http"
)

// Ready reports whether the first config load has completed.
type Ready func() bool

func New(ready Ready, register ...func(*http.ServeMux)) http.Handler {
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
	return recoverer(mux)
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
