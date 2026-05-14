package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alexis/gemline/internal/backplane"
	"github.com/alexis/gemline/internal/db"
	"github.com/alexis/gemline/internal/server"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env (and .env.local if present) before reading any env var so
	// local dev can keep secrets out of the shell. .env.local takes
	// precedence over .env because godotenv.Load doesn't overwrite vars
	// that are already set — we load the override file first.
	// Production deployments won't have these files; godotenv.Load
	// returns "file not found" errors we deliberately ignore.
	_ = godotenv.Load(".env.local")
	_ = godotenv.Load(".env")

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	addr := getenv("ADDR", ":8080")
	dsn := os.Getenv("DATABASE_URL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		repo server.Repository
		bp   *backplane.Backplane
	)
	if dsn != "" {
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			log.Error("database connection failed", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		repo = server.NewPostgresRepo(pool)
		log.Info("persistence enabled", "driver", "postgres")

		// Postgres backplane: LISTEN/NOTIFY bus shared by the WS
		// event scaler and (later) the matchmaking lobby. We only
		// build the object here; Start happens after the server has
		// registered its handlers, so the first session LISTENs on
		// the right set of channels.
		bp = backplane.New(dsn, pool, log)
	} else {
		log.Info("persistence disabled — running with in-memory store only")
	}

	cfg := server.Config{
		SupabaseURL:    os.Getenv("SUPABASE_URL"),
		AllowedOrigins: parseOrigins(os.Getenv("ALLOWED_ORIGINS")),
	}

	store := server.NewStore(repo)
	// Background promotion of public multi-player rooms — flips them to
	// playing once enough seats are occupied and the threshold wait time
	// for that occupancy has elapsed. Tests don't call this so they stay
	// deterministic.
	store.StartMultiPromoter()
	defer store.Close()

	// server.New registers backplane.Subscribe(ChannelGameEvents, …)
	// before we Start the listener — so the first LISTEN session
	// includes the event channel.
	apiServer := server.New(log, store, bp, cfg)
	if bp != nil {
		bp.Start(ctx)
		defer bp.Close()
	}
	// The matcher needs the backplane to be live so its match
	// notifications reach lobby WS subscribers on other pods. Start
	// after Start above. Cancel propagates via ctx on shutdown.
	apiServer.StartMatcher(ctx)

	srv := &http.Server{
		Addr:         addr,
		Handler:      apiServer.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "err", err)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseOrigins turns a comma-separated env var into a trimmed slice. Empty
// or whitespace-only values are skipped so a stray comma can't smuggle in an
// empty entry that would silently match the empty Origin header.
func parseOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
