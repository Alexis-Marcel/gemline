// Package db sets up the Postgres connection pool and runs schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// driverName is the otelsql-wrapped pgx driver name. When the global tracer
// provider is the SDK noop (no OTEL_EXPORTER_OTLP_ENDPOINT), the wrapper is
// essentially free — calls go through but no spans are recorded.
var driverName string

func init() {
	n, err := otelsql.Register("pgx",
		otelsql.WithAttributes(semconv.DBSystemPostgreSQL),
	)
	if err != nil {
		// Programmer mistake (duplicate registration); surface at startup, not later.
		panic(fmt.Errorf("otelsql.Register: %w", err))
	}
	driverName = n
}

// Open opens a connection pool to dsn and applies any pending migrations. The
// caller must Close the returned pool on shutdown.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	pool, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	pool.SetMaxOpenConns(20)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.PingContext(pingCtx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if err := migrate(pool); err != nil {
		_ = pool.Close()
		return nil, err
	}
	return pool, nil
}

func migrate(pool *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(pool, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
