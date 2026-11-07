// Package app wires dependencies and owns the server lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"p2p-signaling-server/internal/admin"
	"p2p-signaling-server/internal/auth"
	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/config"
	"p2p-signaling-server/internal/httpserver"
	"p2p-signaling-server/internal/mcpserver"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/netutil"
	"p2p-signaling-server/internal/ops"
	"p2p-signaling-server/internal/ratelimit"
	"p2p-signaling-server/internal/room"
	"p2p-signaling-server/internal/signaling"
	"p2p-signaling-server/internal/store/mongo"
)

const (
	databaseName  = "main"
	shutdownGrace = 8 * time.Second
	// restartRetryAfter is what clients are told to wait before reconnecting after a shutdown.
	restartRetryAfter = 5 * time.Second
	pollInterval      = 60 * time.Second
	sweepInterval     = 5 * time.Second

	// maxAuthFailuresPerMinute bounds wrong-key attempts per address on the admin API and /mcp.
	maxAuthFailuresPerMinute = 10
)

// Run serves until ctx is cancelled, then shuts down gracefully.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	store, err := mongo.Connect(ctx, cfg.MongoURI, databaseName)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}
	defer store.Close(context.Background())
	buckets := bucket.NewService(store)
	buckets.OnAuditError = func(err error) { log.Error("audit log write failed", "error", err) }
	if len(cfg.TrustedProxyCIDRs) == 0 {
		log.Warn("TRUSTED_PROXY_CIDRS is empty: behind a reverse proxy every player will look like one address, " +
			"and rate limits and lockouts will apply to everyone together. See docs/deploy-caprover.md.")
	}
	if err := buckets.Refresh(ctx); err != nil {
		return fmt.Errorf("initial bucket load: %w", err)
	}
	go buckets.Poll(ctx, pollInterval, func(err error) { log.Warn("bucket refresh failed", "error", err) })

	met := metrics.New()
	rooms := room.NewManager()
	clientIP := func(r *http.Request) netip.Addr { return httpserver.ClientIP(r, cfg.TrustedProxyCIDRs) }
	sig := &signaling.Handler{Buckets: buckets, Rooms: rooms, Log: log, Metrics: met, ClientIP: clientIP}
	go sig.Run(ctx, sweepInterval)
	o := &ops.Ops{Buckets: buckets, Rooms: rooms, Closer: sig, Metrics: met}
	adm := admin.Handler{Ops: o, ClientIP: clientIP}
	authFails := ratelimit.NewLimiter()
	authMW := auth.Middleware(cfg.MasterAPIKey, func(r *http.Request) bool {
		ok := authFails.Allow(netutil.Key(clientIP(r)), maxAuthFailuresPerMinute)
		if !ok {
			met.RateLimited()
		}
		return ok
	})
	mcpHandler := mcpserver.Handler(mcpserver.New(o))
	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		Handler: httpserver.New(log, buckets.Loaded, func(m *http.ServeMux) {
			m.Handle("GET /ws", sig)
			adm.Register(m, authMW)
			m.Handle("/mcp", authMW(mcpHandler))
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "port", cfg.Port)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	// http.Server.Shutdown ignores WebSocket connections, so tell those clients first: they are sent
	// server_restarting and closed, and can reconnect once the new instance is up.
	sig.Shutdown(sctx, restartRetryAfter)
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
