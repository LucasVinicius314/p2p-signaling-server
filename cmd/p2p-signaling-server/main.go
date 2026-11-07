package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"p2p-signaling-server/internal/app"
	"p2p-signaling-server/internal/config"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}
