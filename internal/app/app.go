// Package app wires dependencies and owns the server lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"p2p-signaling-server/internal/admin"
	"p2p-signaling-server/internal/auth"
	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/config"
	"p2p-signaling-server/internal/httpserver"
	"p2p-signaling-server/internal/store/mongo"
)

const (
	shutdownGrace = 5 * time.Second
	pollInterval  = 60 * time.Second
)

// Run serves until ctx is cancelled, then shuts down gracefully.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	store, err := mongo.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}
	defer store.Close(context.Background())
	buckets := bucket.NewService(store)
	if err := buckets.Refresh(ctx); err != nil {
		return fmt.Errorf("initial bucket load: %w", err)
	}
	go buckets.Poll(ctx, pollInterval, func(err error) { log.Warn("bucket refresh failed", "error", err) })

	adm := admin.Handler{Buckets: buckets}
	authMW := auth.Middleware(cfg.MasterAPIKey, nil)
	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		Handler: httpserver.New(buckets.Loaded, func(m *http.ServeMux) {
			adm.Register(m, authMW)
		}),
		ReadHeaderTimeout: 10 * time.Second,
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
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
