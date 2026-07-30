package lease

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"
)

// Each test elects on a unique role name so runs never collide.
func testRole(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "elector-test-" + hex.EncodeToString(b)
	return name
}

func cleanupRole(t *testing.T, pool *sql.DB, name string) {
	t.Cleanup(func() { _, _ = pool.Exec(`DELETE FROM leader_leases WHERE name = $1`, name) })
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestElectorExactlyOneLeader(t *testing.T) {
	pool := testPool(t)
	role := testRole(t)
	cleanupRole(t, pool, role)
	log := testManager(pool, "x").log // reuse the discard logger

	a := NewElector(pool, role, "pod-a", log).WithTTL(500 * time.Millisecond)
	b := NewElector(pool, role, "pod-b", log).WithTTL(500 * time.Millisecond)
	ctx := context.Background()
	a.Start(ctx)
	b.Start(ctx)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)

	waitUntil(t, "a leader elected", func() bool { return a.IsLeader() || b.IsLeader() })
	// Give the loser a couple of campaign rounds to (wrongly) grab it too.
	time.Sleep(400 * time.Millisecond)
	if a.IsLeader() && b.IsLeader() {
		t.Fatal("both electors believe they lead")
	}
}

func TestElectorFailoverOnCrash(t *testing.T) {
	pool := testPool(t)
	role := testRole(t)
	cleanupRole(t, pool, role)
	log := testManager(pool, "x").log

	var aLeads, aDeposed atomic.Bool
	a := NewElector(pool, role, "pod-a", log).WithTTL(300 * time.Millisecond)
	a.OnElected(func(ctx context.Context) {
		aLeads.Store(true)
		<-ctx.Done()
		aDeposed.Store(true)
	})
	// Crash = cancelling a's run context: campaigning stops, the row stays
	// and must expire before anyone else can take over.
	aCtx, crashA := context.WithCancel(context.Background())
	a.Start(aCtx)

	waitUntil(t, "a leads", func() bool { return aLeads.Load() })
	crashA()
	waitUntil(t, "a's workload stopped", func() bool { return aDeposed.Load() })

	b := NewElector(pool, role, "pod-b", log).WithTTL(300 * time.Millisecond)
	var bLeads atomic.Bool
	b.OnElected(func(ctx context.Context) { bLeads.Store(true) })
	b.Start(context.Background())
	t.Cleanup(b.Close)

	waitUntil(t, "b takes over after expiry", func() bool { return bLeads.Load() })
	var epoch int64
	if err := pool.QueryRow(`SELECT epoch FROM leader_leases WHERE name = $1`, role).Scan(&epoch); err != nil || epoch != 2 {
		t.Fatalf("epoch=%d err=%v, want 2 (fencing token must advance on takeover)", epoch, err)
	}
}

func TestElectorResignHandsOverImmediately(t *testing.T) {
	pool := testPool(t)
	role := testRole(t)
	cleanupRole(t, pool, role)
	log := testManager(pool, "x").log

	a := NewElector(pool, role, "pod-a", log)
	a.Start(context.Background())
	waitUntil(t, "a leads", a.IsLeader)
	a.Close() // clean shutdown: resign, no TTL wait

	b := NewElector(pool, role, "pod-b", log).WithTTL(500 * time.Millisecond)
	var bLeads atomic.Bool
	b.OnElected(func(ctx context.Context) { bLeads.Store(true) })
	b.Start(context.Background())
	t.Cleanup(b.Close)
	waitUntil(t, "b leads right after resign", func() bool { return bLeads.Load() })
}
