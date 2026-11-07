// Package admin serves the REST admin API for buckets and the audit log.
package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/ops"
)

const actor = "rest"

type Handler struct {
	Ops *ops.Ops
	// ClientIP, if set, enables GET /admin/v1/client-ip (shows how the server sees the caller).
	ClientIP func(*http.Request) netip.Addr
}

// Register mounts routes under /admin/v1 on mux, wrapped by wrap (auth).
func (h Handler) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	route := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, wrap(f)) }
	route("GET /admin/v1/buckets", h.list)
	route("POST /admin/v1/buckets", h.create)
	route("GET /admin/v1/buckets/{id}", h.get)
	route("PATCH /admin/v1/buckets/{id}", h.patch)
	route("DELETE /admin/v1/buckets/{id}", h.delete)
	route("GET /admin/v1/audit", h.audit)
	route("GET /admin/v1/rooms", h.rooms)
	route("POST /admin/v1/rooms/{bucket}/{code}/close", h.closeRoom)
	route("GET /admin/v1/stats", h.stats)
	route("GET /admin/v1/client-ip", h.clientIP)
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.Ops.Buckets.List())
}

func (h Handler) get(w http.ResponseWriter, r *http.Request) {
	b, ok := h.Ops.Buckets.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, bucket.ErrNotFound)
		return
	}
	writeJSON(w, 200, b)
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	raw, ok := body(w, r)
	if !ok {
		return
	}
	var head struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || !bucket.ValidID(head.ID) {
		writeErr(w, &bucket.ValidationError{Err: errors.New("id must match [a-z0-9-]{3,40}")})
		return
	}
	b, err := h.Ops.Buckets.Create(r.Context(), actor, head.ID, raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, b)
}

func (h Handler) patch(w http.ResponseWriter, r *http.Request) {
	raw, ok := body(w, r)
	if !ok {
		return
	}
	b, err := h.Ops.Buckets.Patch(r.Context(), actor, r.PathValue("id"), raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, b)
}

func (h Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.Ops.DeleteBucket(r.Context(), actor, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) audit(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	e, err := h.Ops.Buckets.RecentAudit(r.Context(), n)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, e)
}

func body(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeErr(w, &bucket.ValidationError{Err: errors.New("unreadable or too large body")})
		return nil, false
	}
	return raw, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ve *bucket.ValidationError
	code := http.StatusInternalServerError
	switch {
	case errors.As(err, &ve):
		code = http.StatusBadRequest
	case errors.Is(err, bucket.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, bucket.ErrExists):
		code = http.StatusConflict
	}
	msg := "internal error"
	if code != http.StatusInternalServerError {
		msg = err.Error()
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func (h Handler) rooms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.Ops.ListRooms(r.URL.Query().Get("bucket")))
}

func (h Handler) closeRoom(w http.ResponseWriter, r *http.Request) {
	if !h.Ops.CloseRoom(r.PathValue("bucket"), r.PathValue("code")) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "room not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) stats(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, h.Ops.Stats()) }

// clientIP reports how the server resolves the caller's address, to verify proxy settings
// (TRUSTED_PROXY_CIDRS) against what Cloudflare and CapRover actually send.
func (h Handler) clientIP(w http.ResponseWriter, r *http.Request) {
	resolved := ""
	if h.ClientIP != nil {
		resolved = h.ClientIP(r).String()
	}
	writeJSON(w, 200, map[string]string{
		"client_ip":       resolved,
		"remote_addr":     r.RemoteAddr,
		"x_forwarded_for": r.Header.Get("X-Forwarded-For"),
	})
}
