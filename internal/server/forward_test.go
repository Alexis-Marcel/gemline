package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/alexis-marcel/gemline/internal/bus"
	"github.com/alexis-marcel/gemline/internal/db"
	"github.com/alexis-marcel/gemline/internal/lease"
)

// Live two-pod integration tests over one shared Postgres, skipped unless
// GEMLINE_TEST_DATABASE_URL is set so plain `go test ./...` stays hermetic.

type testPod struct {
	public   *httptest.Server
	internal *httptest.Server
	leases   *lease.Manager
	store    *Store
}

func forwardTestPool(t *testing.T) *sql.DB {
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

func newTestPod(t *testing.T, pool *sql.DB, name string) *testPod {
	return newTestPodBus(t, pool, name, "")
}

// newTestPodBus builds a full pod (store, server, lease manager, public +
// internal listeners) — with a Redis bus when redisAddr is non-empty.
func newTestPodBus(t *testing.T, pool *sql.DB, name, redisAddr string) *testPod {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStore(NewPostgresRepo(pool))
	t.Cleanup(store.Close)
	var b Bus
	if redisAddr != "" {
		rb, err := bus.NewRedis("redis://"+redisAddr, log)
		if err != nil {
			t.Fatalf("bus.NewRedis: %v", err)
		}
		t.Cleanup(func() { _ = rb.Close() })
		b = rb
	}
	srv, err := New(log, store, b, Config{})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if b != nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		b.Start(ctx)
	}
	internal := httptest.NewServer(srv.InternalRoutes())
	t.Cleanup(internal.Close)
	lm := lease.NewManager(pool, name, log).WithAddr(strings.TrimPrefix(internal.URL, "http://"))
	t.Cleanup(lm.Close)
	store.SetLeaseManager(lm)
	public := httptest.NewServer(srv.Routes())
	t.Cleanup(public.Close)
	return &testPod{public: public, internal: internal, leases: lm, store: store}
}

// newTestGateway builds a gateway pod: forward-only store, read-only lease
// resolver, public listener only — no lease manager, no internal listener.
func newTestGateway(t *testing.T, pool *sql.DB, gamesvcAddr string) *testPod {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStore(NewPostgresRepo(pool))
	t.Cleanup(store.Close)
	srv, err := New(log, store, nil, Config{
		ForwardTo: gamesvcAddr,
		Resolver:  lease.NewResolver(pool),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	public := httptest.NewServer(srv.Routes())
	t.Cleanup(public.Close)
	return &testPod{public: public, store: store}
}

func TestGatewayRoutesToGamesvc(t *testing.T) {
	pool := forwardTestPool(t)
	gamesvc := newTestPod(t, pool, "gamesvc-1")
	gw := newTestGateway(t, pool, strings.TrimPrefix(gamesvc.internal.URL, "http://"))

	// Create through the gateway: proxied to the pool, so the gamesvc claims
	// the lease and the gateway never runs game logic.
	gameID, tokenAlice := createGameOn(t, pool, gw)
	if _, held := gamesvc.leases.Held(gameID); !held {
		t.Fatal("gamesvc did not claim the created game")
	}

	// Join via the gateway: resolver finds the owner, command lands there —
	// the clock must run on the gamesvc, never on the gateway.
	if status, _ := postJSON(t, gw.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil); status != http.StatusCreated {
		t.Fatalf("join via gateway: status %d", status)
	}
	if !hasClockTimer(gamesvc.store, gameID) {
		t.Fatal("gamesvc has no clock timer after start")
	}
	if hasClockTimer(gw.store, gameID) {
		t.Fatal("gateway armed a clock timer — forward-only mode is broken")
	}

	if status, _ := postJSON(t, gw.public.URL+"/api/games/"+gameID+"/moves",
		map[string]any{"q": 0, "r": 0}, map[string]string{"X-Player-Token": tokenAlice}); status != http.StatusOK {
		t.Fatalf("move via gateway: status %d", status)
	}
}

func TestGatewayHandlesLocallyWhenPoolUnreachable(t *testing.T) {
	pool := forwardTestPool(t)
	gamesvc := newTestPod(t, pool, "gamesvc-1")
	gw := newTestGateway(t, pool, strings.TrimPrefix(gamesvc.internal.URL, "http://"))

	gameID, _ := createGameOn(t, pool, gw)

	// The whole gamesvc tier dies (owner and pool are the same listener
	// here). The degradation chain owner → pool → local must still serve
	// the player.
	gamesvc.internal.Close()
	status, body := postJSON(t, gw.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("join with dead gamesvc tier: status %d", status)
	}
	var token string
	if err := json.Unmarshal(body["token"], &token); err != nil || token == "" {
		t.Fatalf("join with dead gamesvc tier: no token (err=%v)", err)
	}
}

func postJSON(t *testing.T, url string, body any, headers map[string]string) (int, map[string]json.RawMessage) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func createGameOn(t *testing.T, pool *sql.DB, pod *testPod) (gameID, token string) {
	t.Helper()
	status, body := postJSON(t, pod.public.URL+"/api/games", map[string]any{"players": 2, "name": "Alice"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create game: status %d", status)
	}
	var game struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body["game"], &game); err != nil {
		t.Fatalf("decode game: %v", err)
	}
	if err := json.Unmarshal(body["token"], &token); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(`DELETE FROM games WHERE id = $1`, game.ID) })
	return game.ID, token
}

func TestCommandForwardedToOwner(t *testing.T) {
	pool := forwardTestPool(t)
	podA := newTestPod(t, pool, "pod-a")
	podB := newTestPod(t, pool, "pod-b")

	gameID, tokenAlice := createGameOn(t, pool, podA)
	if _, held := podA.leases.Held(gameID); !held {
		t.Fatal("creator pod does not hold the lease")
	}

	// Bob joins through pod B: B must forward to A, not claim or handle.
	status, body := postJSON(t, podB.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("join via B: status %d", status)
	}
	var tokenBob string
	if err := json.Unmarshal(body["token"], &tokenBob); err != nil || tokenBob == "" {
		t.Fatalf("join via B: no token (err=%v)", err)
	}
	if _, held := podB.leases.Held(gameID); held {
		t.Fatal("B claimed the lease instead of forwarding")
	}

	// The join was executed on A: its in-memory record must already show Bob
	// without any reload (a local execution on B could not update A's cache).
	rec, ok, err := podA.store.Get(context.Background(), gameID)
	if err != nil || !ok {
		t.Fatalf("A store get: ok=%v err=%v", ok, err)
	}
	rec.Lock()
	occupied := 0
	for _, s := range rec.Seats {
		if s.Occupied {
			occupied++
		}
	}
	rec.Unlock()
	if occupied != 2 {
		t.Fatalf("A's record shows %d occupied seats, want 2", occupied)
	}

	// Alice plays through B as well; the move must land on A the same way.
	status, _ = postJSON(t, podB.public.URL+"/api/games/"+gameID+"/moves",
		map[string]any{"q": 0, "r": 0}, map[string]string{"X-Player-Token": tokenAlice})
	if status != http.StatusOK {
		t.Fatalf("move via B: status %d", status)
	}
}

func TestForwardFallsBackToLocalWhenOwnerUnreachable(t *testing.T) {
	pool := forwardTestPool(t)
	podA := newTestPod(t, pool, "pod-a")
	podB := newTestPod(t, pool, "pod-b")

	gameID, _ := createGameOn(t, pool, podA)

	// A holds a live lease but its internal listener is gone (crashed pod,
	// TTL not yet expired). B's forward must fail at dial time and replay
	// the command locally — the player sees a success, not a 502.
	podA.internal.Close()

	status, body := postJSON(t, podB.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("join via B with dead owner: status %d", status)
	}
	var token string
	if err := json.Unmarshal(body["token"], &token); err != nil || token == "" {
		t.Fatalf("join via B with dead owner: no token (err=%v)", err)
	}
}

func hasCached(s *Store, gameID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.games[gameID]
	return ok
}

// TestRedisBusInvalidatesRemoteCache is the step-2 flow end to end: pod B
// caches a game it doesn't own (watching its Redis channel), a write lands on
// owner A, and A's publish must reach B and drop its now-stale cache.
func TestRedisBusInvalidatesRemoteCache(t *testing.T) {
	pool := forwardTestPool(t)
	mr := miniredis.RunT(t)
	podA := newTestPodBus(t, pool, "pod-a", mr.Addr())
	podB := newTestPodBus(t, pool, "pod-b", mr.Addr())

	gameID, tokenAlice := createGameOn(t, pool, podA)
	if status, _ := postJSON(t, podA.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil); status != http.StatusCreated {
		t.Fatalf("join: status %d", status)
	}

	// A read via B fills B's cache — and, through the bus interest hook,
	// subscribes B to this game's channel.
	resp, err := http.Get(podB.public.URL + "/api/games/" + gameID)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get via B: %v/%d", err, resp.StatusCode)
	}
	resp.Body.Close()
	if !hasCached(podB.store, gameID) {
		t.Fatal("B did not cache the game")
	}

	// Alice plays through B: the command forwards to owner A, A appends and
	// publishes on game:{id}; B is watching and must invalidate its cache.
	if status, _ := postJSON(t, podB.public.URL+"/api/games/"+gameID+"/moves",
		map[string]any{"q": 0, "r": 0}, map[string]string{"X-Player-Token": tokenAlice}); status != http.StatusOK {
		t.Fatalf("move via B: status %d", status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hasCached(podB.store, gameID) {
		time.Sleep(10 * time.Millisecond)
	}
	if hasCached(podB.store, gameID) {
		t.Fatal("B's stale cache survived the owner's publish — invalidation not routed")
	}
}

// hasClockTimer peeks at the unexported clock map — same-package test only.
func hasClockTimer(s *Store, gameID string) bool {
	s.clocks.mu.Lock()
	defer s.clocks.mu.Unlock()
	_, ok := s.clocks.cancels[gameID]
	return ok
}

func TestClockArmedOnlyOnOwner(t *testing.T) {
	pool := forwardTestPool(t)
	podA := newTestPod(t, pool, "pod-a")
	podB := newTestPod(t, pool, "pod-b")

	gameID, _ := createGameOn(t, pool, podA)
	// Join through A directly so the game starts (2 seats → playing) with A
	// as owner; the default config runs a 10-minute chess clock.
	if status, _ := postJSON(t, podA.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil); status != http.StatusCreated {
		t.Fatalf("join: status %d", status)
	}
	if !hasClockTimer(podA.store, gameID) {
		t.Fatal("owner has no clock timer after start")
	}

	// A read on B caches the game there; before 1d this armed a duplicate
	// flag timer — the original bug.
	resp, err := http.Get(podB.public.URL + "/api/games/" + gameID)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get via B: %v/%d", err, resp.StatusCode)
	}
	resp.Body.Close()
	if hasClockTimer(podB.store, gameID) {
		t.Fatal("non-owner armed a clock timer (duplicate-timer bug)")
	}
}

func TestOrphanSweeperAdoptsGame(t *testing.T) {
	pool := forwardTestPool(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Pod A owns a playing game, then dies without handover: no heartbeat, no
	// release — only its 150ms TTL runs out.
	podA := newTestPod(t, pool, "pod-a")
	podA.leases.WithTTL(150 * time.Millisecond)
	gameID, _ := createGameOn(t, pool, podA)
	if status, _ := postJSON(t, podA.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil); status != http.StatusCreated {
		t.Fatalf("join: status %d", status)
	}
	// Re-acquire so the short TTL applies, then "crash" A.
	if _, acquired, err := podA.leases.TryAcquire(context.Background(), gameID); err != nil || !acquired {
		t.Fatalf("short-ttl reacquire: %v/%v", acquired, err)
	}

	// Pod B never saw this game. Its sweeper alone must adopt it.
	storeB := NewStore(NewPostgresRepo(pool))
	t.Cleanup(storeB.Close)
	lmB := lease.NewManager(pool, "pod-b-sweeper", log)
	t.Cleanup(lmB.Close)
	storeB.SetLeaseManager(lmB)

	time.Sleep(300 * time.Millisecond) // let A's lease expire
	ids, err := storeB.repo.OrphanPlayingGames(context.Background(), 10)
	if err != nil {
		t.Fatalf("orphan query: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == gameID {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan query missed the game (got %v)", ids)
	}

	storeB.AdoptGame(context.Background(), gameID)
	if epoch, held := lmB.Held(gameID); !held || epoch != 2 {
		t.Fatalf("B after adopt: held=%v epoch=%d, want true/2", held, epoch)
	}
	if !hasClockTimer(storeB, gameID) {
		t.Fatal("adopted game has no clock timer — the takeover didn't re-arm")
	}
}

// TestStaleEpochWriteFencedOff is the split-brain scenario end to end: a pod
// that lost its lease (GC pause past the TTL) must have its journal writes
// rejected once a new owner has taken over.
func TestStaleEpochWriteFencedOff(t *testing.T) {
	pool := forwardTestPool(t)
	repo := NewPostgresRepo(pool)
	ctx := context.Background()

	store := NewStore(repo)
	t.Cleanup(store.Close)
	rec, err := store.Create(ctx, 2, VisibilityPrivate)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(`DELETE FROM games WHERE id = $1`, rec.ID) })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	zombie := lease.NewManager(pool, "pod-zombie", log).WithTTL(100 * time.Millisecond)
	successor := lease.NewManager(pool, "pod-successor", log)
	t.Cleanup(successor.Close)

	epoch1, acquired, err := zombie.TryAcquire(ctx, rec.ID)
	if err != nil || !acquired || epoch1 != 1 {
		t.Fatalf("zombie acquire: epoch=%d acquired=%v err=%v", epoch1, acquired, err)
	}
	// Zombie freezes (no heartbeat); its lease expires and a new pod takes over.
	time.Sleep(250 * time.Millisecond)
	epoch2, acquired, err := successor.TryAcquire(ctx, rec.ID)
	if err != nil || !acquired || epoch2 != 2 {
		t.Fatalf("successor takeover: epoch=%d acquired=%v err=%v", epoch2, acquired, err)
	}

	// The zombie wakes up and writes with the epoch it still believes in.
	if _, err := repo.AppendEvent(ctx, rec.ID, "state", []byte(`{}`), epoch1); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("zombie write: err=%v, want ErrStaleLease", err)
	}
	// The rightful owner writes fine, and unfenced writes (epoch 0) still work.
	if _, err := repo.AppendEvent(ctx, rec.ID, "state", []byte(`{}`), epoch2); err != nil {
		t.Fatalf("successor write: %v", err)
	}
	if _, err := repo.AppendEvent(ctx, rec.ID, "state", []byte(`{}`), 0); err != nil {
		t.Fatalf("unfenced write: %v", err)
	}
}

func TestCommandClaimsFreeLease(t *testing.T) {
	pool := forwardTestPool(t)
	podA := newTestPod(t, pool, "pod-a")
	podB := newTestPod(t, pool, "pod-b")

	gameID, _ := createGameOn(t, pool, podA)

	// A releases (clean-shutdown situation): the next command through B must
	// claim ownership locally instead of forwarding into the void.
	podA.leases.Release(context.Background(), gameID)

	status, body := postJSON(t, podB.public.URL+"/api/games/"+gameID+"/join",
		map[string]any{"name": "Bob"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("join via B: status %d (body %v)", status, body)
	}
	if _, held := podB.leases.Held(gameID); !held {
		t.Fatal("B did not claim the free lease")
	}
}
