// Package auth provides master-key middleware.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Middleware requires "Authorization: Bearer <key>". onFail, if non-nil, is called on each
// failed attempt (e.g. a per-IP limiter); returning false answers 429 instead of 401.
func Middleware(key string, onFail func(r *http.Request) (allowed bool)) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(key))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			tok, ok := strings.CutPrefix(h, "Bearer ")
			got := sha256.Sum256([]byte(tok))
			if !ok || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				if onFail != nil && !onFail(r) {
					http.Error(w, "too many attempts", http.StatusTooManyRequests)
					return
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
