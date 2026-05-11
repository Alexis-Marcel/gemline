package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexis/gemline/internal/db"
	"github.com/alexis/gemline/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	addr := getenv("ADDR", ":8080")
	dsn := os.Getenv("DATABASE_URL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var repo server.Repository
	if dsn != "" {
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			log.Error("database connection failed", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		repo = server.NewPostgresRepo(pool)
		log.Info("persistence enabled", "driver", "postgres")
	} else {
		log.Info("persistence disabled — running with in-memory store only")
	}

	cfg := server.Config{
		JWTSecret: os.Getenv("SUPABASE_JWT_SECRET"),
	}
	if cfg.JWTSecret == "" {
		log.Warn("SUPABASE_JWT_SECRET not set — user auth endpoints will respond 401")
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      server.New(log, server.NewStore(repo), cfg).Routes(),
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
