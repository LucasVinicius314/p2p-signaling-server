// Command devserver is a local signaling server for developing and testing a game against this project.
//
// It keeps its buckets in memory, so it needs no MongoDB, has no admin API or MCP, and forgets everything when it
// stops. It runs the same WebSocket handler as the real server, so a game behaves the same against it. Never run it on a
// public address.
//
//	go run ./cmd/devserver -bucket my-game
//
// Then point the game at http://127.0.0.1:8099 and use the bucket id you gave. See docs/integration-guide.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/room"
	"p2p-signaling-server/internal/signaling"
	"p2p-signaling-server/internal/store/memory"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on (keep it on loopback)")
	buckets := flag.String("bucket", "game", "bucket ids to create, comma separated")
	topology := flag.String("topology", "star", "star or mesh")
	maxPlayers := flag.Int("max-players", 8, "players per room")
	verbose := flag.Bool("v", false, "log per-connection detail")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	if err := run(*addr, split(*buckets), *topology, *maxPlayers, log); err != nil {
		log.Error("devserver failed", "error", err)
		os.Exit(1)
	}
}

func run(addr string, ids []string, topology string, maxPlayers int, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := bucket.NewService(memory.New())
	for _, id := range ids {
		overlay, _ := json.Marshal(map[string]any{
			"name": id, "topology": topology, "max_players": maxPlayers,
		})
		if _, err := svc.Create(ctx, "devserver", id, overlay); err != nil {
			return fmt.Errorf("bucket %q: %w", id, err)
		}
	}

	sig := &signaling.Handler{Buckets: svc, Rooms: room.NewManager(), Log: log}
	go sig.Run(ctx, 5*time.Second)

	mux := http.NewServeMux()
	mux.Handle("GET /ws", sig)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("dev server listening", "url", "http://"+addr, "buckets", ids, "topology", topology)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sig.Shutdown(sctx, time.Second)
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
