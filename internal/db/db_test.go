package db

import (
	"context"
	"os"
	"testing"
)

// TestOpen_AppliesMigrations is a live integration test, skipped unless
// GEMLINE_TEST_DATABASE_URL is set so plain `go test ./...` stays hermetic.
func TestOpen_AppliesMigrations(t *testing.T) {
	dsn := os.Getenv("GEMLINE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GEMLINE_TEST_DATABASE_URL not set; skipping integration test")
	}

	pool, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	for _, table := range []string{"games", "seats", "moves"} {
		var n int
		err := pool.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if err != nil {
			t.Errorf("query %s: %v", table, err)
		}
	}
}
