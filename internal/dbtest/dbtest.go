// Package dbtest serializes DB-backed test packages sharing one Postgres.
//
// `go test ./...` runs packages in parallel, and the server package's
// integration tests TRUNCATE the shared tables between cases — wiping any
// row another package is testing against (game_leases go with games via
// CASCADE). A session advisory lock held for the whole package run makes
// cross-package interleaving impossible while keeping intra-package order
// untouched.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// lockKey is arbitrary but must be shared by every package taking the lock.
const lockKey = 0x67656d6c696e65 // "gemline"

// Lock takes the advisory lock for the whole test binary; call the returned
// unlock in TestMain after m.Run. No-op when GEMLINE_TEST_DATABASE_URL is
// unset (integration tests skip anyway).
func Lock() (unlock func()) {
	dsn := os.Getenv("GEMLINE_TEST_DATABASE_URL")
	if dsn == "" {
		return func() {}
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: open: %v\n", err)
		os.Exit(1)
	}
	// Advisory locks are session-scoped: pin one connection and hold it.
	conn, err := pool.Conn(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: conn: %v\n", err)
		os.Exit(1)
	}
	if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: advisory lock: %v\n", err)
		os.Exit(1)
	}
	return func() {
		_ = conn.Close() // session ends, lock releases
		_ = pool.Close()
	}
}
