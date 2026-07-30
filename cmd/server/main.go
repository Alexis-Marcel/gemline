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

	"github.com/alexis-marcel/gemline/internal/bus"
	"github.com/alexis-marcel/gemline/internal/db"
	"github.com/alexis-marcel/gemline/internal/lease"
	"github.com/alexis-marcel/gemline/internal/server"
	"github.com/alexis-marcel/gemline/internal/tracing"
	"github.com/joho/godotenv"
)

// version is overridable via -ldflags at build time; "dev" is the local default.
var version = "dev"

func main() {
	// .env.local first: godotenv.Load doesn't overwrite already-set vars,
	// so the override file must win over .env. Missing files are ignored.
	_ = godotenv.Load(".env.local")
	_ = godotenv.Load(".env")

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	addr := getenv("ADDR", ":8080")
	internalAddr := getenv("INTERNAL_ADDR", ":8090")
	dsn := os.Getenv("DATABASE_URL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Tracing must come up before anything that emits spans (db.Open does, via
	// the otelsql-wrapped driver). Setup is a no-op when OTEL_EXPORTER_OTLP_ENDPOINT
	// is unset, so dev runs without a collector still work.
	shutdownTracing, err := tracing.Setup(ctx, "gemline-server", version)
	if err != nil {
		log.Error("tracing setup failed", "err", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Error("tracing shutdown", "err", err)
		}
	}()

	var (
		repo     server.Repository
		eventBus server.Bus
		leases   *lease.Manager
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
		// Fan-out: Redis routes events per game, so pods receive only the
		// games they serve. Leases stay in Postgres — the fencing check must
		// be atomic with the fenced write, in the same database.
		redisURL := os.Getenv("REDIS_URL")
		if redisURL == "" {
			log.Error("REDIS_URL is required with DATABASE_URL (docker compose up -d starts one locally)")
			os.Exit(1)
		}
		rb, err := bus.NewRedis(redisURL, log)
		if err != nil {
			log.Error("redis bus init failed", "err", err)
			os.Exit(1)
		}
		eventBus = rb
		leases = lease.NewManager(pool, lease.NewOwnerID(), log).
			WithAddr(advertiseAddr(os.Getenv("ADVERTISE_ADDR"), internalAddr))
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
	if leases != nil {
		// Wire the store first: the lease-lost callback must exist before the
		// first heartbeat can detect a takeover.
		store.SetLeaseManager(leases)
		leases.Start(ctx)
		// Deferred before pool.Close (LIFO), so the release-all DELETE still
		// has a live pool; a clean shutdown hands games over immediately.
		defer leases.Close()
		store.StartOrphanSweeper(ctx, log)
		log.Info("lease manager started", "owner", leases.Owner())
	}

	// server.New registers the bus handlers; Start the listener only
	// afterwards so the first session subscribes to the right channels.
	apiServer, err := server.New(log, store, eventBus, cfg)
	if err != nil {
		log.Error("server init failed", "err", err)
		os.Exit(1)
	}
	if eventBus != nil {
		eventBus.Start(ctx)
		defer eventBus.Close()
	}
	// Start after the bus is live so match notifications reach lobby
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

	// Pod-to-pod command forwarding: only meaningful when leases are on, and
	// deliberately absent from the public Service/ingress.
	var internalSrv *http.Server
	if leases != nil {
		internalSrv = &http.Server{
			Addr:         internalAddr,
			Handler:      apiServer.InternalRoutes(),
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		go func() {
			log.Info("internal listener up", "addr", internalAddr, "advertise", leases.Addr())
			if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("internal listener error", "err", err)
				os.Exit(1)
			}
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "err", err)
	}
	if internalSrv != nil {
		if err := internalSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("internal shutdown error", "err", err)
		}
	}
}

// advertiseAddr resolves what sibling pods should dial to reach this pod's
// internal listener: ADVERTISE_ADDR when set (k8s: "$(POD_IP):8090"), else
// loopback + the internal port — right for multi-process local runs.
func advertiseAddr(advertise, listen string) string {
	if advertise != "" {
		return advertise
	}
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
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
