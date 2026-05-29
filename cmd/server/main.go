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
	// .env.local first: godotenv.Load doesn't overwrite already-set vars,
	// so the override file must win over .env. Missing files are ignored.
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
		bp = backplane.New(dsn, pool, log)
	} else {
		log.Info("persistence disabled — running with in-memory store only")
	}

	cfg := server.Config{
		SupabaseURL:    os.Getenv("SUPABASE_URL"),
		AllowedOrigins: parseOrigins(os.Getenv("ALLOWED_ORIGINS")),
	}

	store := server.NewStore(repo)
	store.StartStaleGameCleaner(log)
	defer store.Close()

	// server.New registers the backplane handlers; Start the listener only
	// afterwards so the first LISTEN session subscribes to the right channels.
	apiServer := server.New(log, store, bp, cfg)
	if bp != nil {
		bp.Start(ctx)
		defer bp.Close()
	}
	// Start after the backplane is live so match notifications reach lobby
	// subscribers on other pods.
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

// parseOrigins splits a comma-separated list, dropping empty entries so a
// stray comma can't smuggle in a "" that would match the empty Origin header.
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
