package lease

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alexis-marcel/gemline/internal/db"
)

// Live integration tests, skipped unless GEMLINE_TEST_DATABASE_URL is set so
// plain `go test ./...` stays hermetic.
func testPool(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GEMLINE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GEMLINE_TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func testManager(pool *sql.DB, owner string) *Manager {
	return NewManager(pool, owner, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// createGame inserts the minimal games row the lease FK requires.
func createGame(t *testing.T, pool *sql.DB) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	id := "lease-test-" + hex.EncodeToString(b)
	_, err := pool.Exec(`INSERT INTO games (id, status, board_side, capture_pairs_win, align4_to_win, align5_to_win)
	                     VALUES ($1, 'waiting', 11, 0, 0, 0)`, id)
	if err != nil {
		t.Fatalf("insert game: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(`DELETE FROM games WHERE id = $1`, id) })
	return id
}

func TestAcquireAndRenewSameOwner(t *testing.T) {
	pool := testPool(t)
	gameID := createGame(t, pool)
	ctx := context.Background()
	a := testManager(pool, "pod-a")

	epoch, acquired, err := a.TryAcquire(ctx, gameID)
	if err != nil || !acquired || epoch != 1 {
		t.Fatalf("fresh acquire: epoch=%d acquired=%v err=%v, want 1/true/nil", epoch, acquired, err)
	}
	if e, ok := a.Held(gameID); !ok || e != 1 {
		t.Fatalf("Held: %d/%v, want 1/true", e, ok)
	}

	// Re-acquiring one's own lease is a renewal: epoch must not move.
	epoch, acquired, err = a.TryAcquire(ctx, gameID)
	if err != nil || !acquired || epoch != 1 {
		t.Fatalf("renewal: epoch=%d acquired=%v err=%v, want 1/true/nil", epoch, acquired, err)
	}
}

func TestContentionWhileLive(t *testing.T) {
	pool := testPool(t)
	gameID := createGame(t, pool)
	ctx := context.Background()
	a := testManager(pool, "pod-a").WithAddr("addr-a")
	b := testManager(pool, "pod-b")

	if _, acquired, err := a.TryAcquire(ctx, gameID); err != nil || !acquired {
		t.Fatalf("a acquire: %v/%v", acquired, err)
	}
	g, err := b.Acquire(ctx, gameID)
	if err != nil {
		t.Fatalf("b acquire: %v", err)
	}
	if g.Acquired {
		t.Fatalf("b stole a live lease (epoch %d)", g.Epoch)
	}
	if g.Owner != "pod-a" || g.Addr != "addr-a" || g.Epoch != 1 {
		t.Fatalf("Grant: %+v, want owner pod-a addr addr-a epoch 1", g)
	}
}

func TestTakeoverAfterExpiry(t *testing.T) {
	pool := testPool(t)
	gameID := createGame(t, pool)
	ctx := context.Background()
	a := testManager(pool, "pod-a").WithTTL(200 * time.Millisecond)
	b := testManager(pool, "pod-b")

	if _, acquired, err := a.TryAcquire(ctx, gameID); err != nil || !acquired {
		t.Fatalf("a acquire: %v/%v", acquired, err)
	}
	time.Sleep(400 * time.Millisecond) // no heartbeat running: let it expire

	epoch, acquired, err := b.TryAcquire(ctx, gameID)
	if err != nil || !acquired {
		t.Fatalf("b takeover: acquired=%v err=%v", acquired, err)
	}
	if epoch != 2 {
		t.Fatalf("takeover epoch=%d, want 2 (fencing token must advance)", epoch)
	}
}

func TestReleaseHandsOverImmediately(t *testing.T) {
	pool := testPool(t)
	gameID := createGame(t, pool)
	ctx := context.Background()
	a := testManager(pool, "pod-a")
	b := testManager(pool, "pod-b")

	if _, acquired, err := a.TryAcquire(ctx, gameID); err != nil || !acquired {
		t.Fatalf("a acquire: %v/%v", acquired, err)
	}
	a.Release(ctx, gameID)
	if _, ok := a.Held(gameID); ok {
		t.Fatal("a still believes it holds a released lease")
	}

	epoch, acquired, err := b.TryAcquire(ctx, gameID)
	if err != nil || !acquired || epoch != 1 {
		t.Fatalf("b after release: epoch=%d acquired=%v err=%v, want 1/true/nil (fresh insert)", epoch, acquired, err)
	}
}

func TestHeartbeatKeepsLeaseAlive(t *testing.T) {
	pool := testPool(t)
	gameID := createGame(t, pool)
	ctx := context.Background()
	a := testManager(pool, "pod-a").WithTTL(300 * time.Millisecond)
	b := testManager(pool, "pod-b")

	if _, acquired, err := a.TryAcquire(ctx, gameID); err != nil || !acquired {
		t.Fatalf("a acquire: %v/%v", acquired, err)
	}
	a.Start(ctx)
	defer a.Close()

	// Well past the TTL: only the heartbeat can be keeping the lease alive.
	time.Sleep(900 * time.Millisecond)

	if _, acquired, _ := b.TryAcquire(ctx, gameID); acquired {
		t.Fatal("b acquired a heartbeat-renewed lease")
	}
}

func TestCloseReleasesEverything(t *testing.T) {
	pool := testPool(t)
	g1, g2 := createGame(t, pool), createGame(t, pool)
	ctx := context.Background()
	a := testManager(pool, "pod-a")
	b := testManager(pool, "pod-b")

	for _, g := range []string{g1, g2} {
		if _, acquired, err := a.TryAcquire(ctx, g); err != nil || !acquired {
			t.Fatalf("a acquire %s: %v/%v", g, acquired, err)
		}
	}
	a.Close()

	for _, g := range []string{g1, g2} {
		if _, acquired, err := b.TryAcquire(ctx, g); err != nil || !acquired {
			t.Fatalf("b after close %s: acquired=%v err=%v", g, acquired, err)
		}
	}
}
