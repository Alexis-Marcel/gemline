package server

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/alexis/gemline/internal/db"
	"github.com/alexis/gemline/internal/game"
)

// Integration tests for the Postgres repository. Skipped unless
// GEMLINE_TEST_DATABASE_URL points at a running Postgres.

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GEMLINE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GEMLINE_TEST_DATABASE_URL not set; skipping repo integration test")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	// Truncate between tests so they stay order-independent.
	if _, err := pool.Exec("TRUNCATE moves, seats, games CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func TestPostgresRepo_CreateJoinPlay_RoundTrip(t *testing.T) {
	pool := openTestDB(t)
	repo := NewPostgresRepo(pool)
	ctx := context.Background()

	// Two stores: the first plays, the second loads fresh from the DB —
	// simulating a server restart with a warm DB.
	first := NewStore(repo)
	rec, err := first.Create(ctx, 2, VisibilityPrivate)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gameID := rec.ID

	_, tokA, err := first.Join(ctx, gameID, "Alice", "", -1)
	if err != nil {
		t.Fatalf("Join Alice: %v", err)
	}
	_, _, err = first.Join(ctx, gameID, "Bob", "", -1)
	if err != nil {
		t.Fatalf("Join Bob: %v", err)
	}

	if _, _, err := first.PlayMove(ctx, gameID, tokA, 0, 0); err != nil {
		t.Fatalf("PlayMove Alice: %v", err)
	}

	second := NewStore(repo)
	loaded, ok, err := second.Get(ctx, gameID)
	if err != nil {
		t.Fatalf("Get reloaded: %v", err)
	}
	if !ok {
		t.Fatal("expected reloaded game to exist")
	}

	if loaded.Status != StatusPlaying {
		t.Errorf("status after reload = %s, want playing", loaded.Status)
	}
	if loaded.State.Turn != 1 {
		t.Errorf("turn after reload = %d, want 1 (Bob's turn)", loaded.State.Turn)
	}
	if len(loaded.State.History) != 1 {
		t.Errorf("history length after reload = %d, want 1", len(loaded.State.History))
	}
	if loaded.State.Players[0].GemsRemaining != game.GemsPerPlayer-1 {
		t.Errorf("Alice gems remaining after reload = %d, want %d",
			loaded.State.Players[0].GemsRemaining, game.GemsPerPlayer-1)
	}
	if loaded.Seats[0].Name != "Alice" || !loaded.Seats[0].Occupied {
		t.Errorf("seat 0 after reload = %+v", loaded.Seats[0])
	}

	// Alice's token must still authenticate after reload; it's Bob's turn now,
	// so the move is rejected with ErrWrongTurn (proving the token hashed back).
	if _, _, err := second.PlayMove(ctx, gameID, tokA, 1, 0); err == nil {
		t.Fatal("expected ErrWrongTurn since it's Bob's turn after the first move replayed")
	}
}

func TestPostgresRepo_CaptureSurvivesReload(t *testing.T) {
	pool := openTestDB(t)
	repo := NewPostgresRepo(pool)
	ctx := context.Background()

	first := NewStore(repo)
	rec, err := first.Create(ctx, 2, VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	gameID := rec.ID

	_, tokA, _ := first.Join(ctx, gameID, "Alice", "", -1)
	_, tokB, _ := first.Join(ctx, gameID, "Bob", "", -1)

	// Horizontal capture: Alice's (1,0) sandwiches Bob's (-1,0) and (0,0).
	plays := []struct {
		token string
		q, r  int
	}{
		{tokA, -2, 0},
		{tokB, -1, 0},
		{tokA, 5, -2},
		{tokB, 0, 0},
		{tokA, 1, 0}, // capture
	}
	for i, p := range plays {
		if _, _, err := first.PlayMove(ctx, gameID, p.token, p.q, p.r); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if got := rec.State.Players[0].CapturedPairs; got != 1 {
		t.Fatalf("pre-reload: Alice captured pairs = %d, want 1", got)
	}

	// The ApplyMove replay on reload must reproduce the capture.
	second := NewStore(repo)
	loaded, _, err := second.Get(ctx, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.State.Players[0].CapturedPairs; got != 1 {
		t.Fatalf("post-reload: Alice captured pairs = %d, want 1", got)
	}
	if loaded.State.Board.At(game.Position{Q: -1, R: 0}) != game.Empty {
		t.Fatalf("post-reload: captured cell (-1,0) should be Empty")
	}
	if loaded.State.Board.At(game.Position{Q: 0, R: 0}) != game.Empty {
		t.Fatalf("post-reload: captured cell (0,0) should be Empty")
	}
}

// TestPostgresRepo_RematchOffer_SurvivesCacheInvalidation guards a multi-pod
// regression: pod A records the proposer's offer in memory, pod B invalidates
// its cache and reloads from Postgres. The offer must be persisted so pod B
// completes it rather than opening a fresh one with only the acceptor.
func TestPostgresRepo_RematchOffer_SurvivesCacheInvalidation(t *testing.T) {
	pool := openTestDB(t)
	repo := NewPostgresRepo(pool)
	ctx := context.Background()

	podA := NewStore(repo)
	rec, err := podA.Create(ctx, 2, VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	gameID := rec.ID

	_, aliceTok, err := podA.Join(ctx, gameID, "Alice", "", -1)
	if err != nil {
		t.Fatal(err)
	}
	_, bobTok, err := podA.Join(ctx, gameID, "Bob", "", -1)
	if err != nil {
		t.Fatal(err)
	}
	// End the game so OfferRematch's StatusFinished gate passes.
	if _, err := podA.Resign(ctx, gameID, aliceTok); err != nil {
		t.Fatalf("Resign: %v", err)
	}

	if _, err := podA.OfferRematch(ctx, gameID, aliceTok); err != nil {
		t.Fatalf("Alice's OfferRematch on pod A: %v", err)
	}

	// A separate Store = pod B's process: same DB, its own cache, post-NOTIFY.
	podB := NewStore(repo)
	if _, err := podB.OfferRematch(ctx, gameID, bobTok); err != nil {
		t.Fatalf("Bob's OfferRematch on pod B: %v", err)
	}

	got, ok, err := podB.Get(ctx, gameID)
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	got.Lock()
	rematchID := got.RematchGameID
	hasOffer := got.RematchOffer != nil
	got.Unlock()
	if rematchID == "" {
		t.Fatalf("rematch must be created once both players have accepted across pods; got empty RematchGameID")
	}
	if hasOffer {
		t.Fatalf("offer must be cleared after the rematch is created, still got %+v", got.RematchOffer)
	}
}

// TestPostgresRepo_DrawOffer_SurvivesCacheInvalidation is the draw-offer twin
// of the rematch test: the offer must persist so an accept landing on a
// different pod doesn't 409 with ErrDrawNotOffered (the "Accepter trop vite"
// bug). The pre-fix workaround only worked when a retry hit the origin pod.
func TestPostgresRepo_DrawOffer_SurvivesCacheInvalidation(t *testing.T) {
	pool := openTestDB(t)
	repo := NewPostgresRepo(pool)
	ctx := context.Background()

	podA := NewStore(repo)
	rec, err := podA.Create(ctx, 2, VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	gameID := rec.ID

	_, aliceTok, err := podA.Join(ctx, gameID, "Alice", "", -1)
	if err != nil {
		t.Fatal(err)
	}
	_, bobTok, err := podA.Join(ctx, gameID, "Bob", "", -1)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := podA.OfferDraw(ctx, gameID, aliceTok); err != nil {
		t.Fatalf("Alice's OfferDraw on pod A: %v", err)
	}

	// Pod B reloads through the repo; pre-fix, draw_offer_by came back as -1.
	podB := NewStore(repo)
	if _, err := podB.AcceptDraw(ctx, gameID, bobTok); err != nil {
		t.Fatalf("Bob's AcceptDraw on pod B (multi-pod regression): %v", err)
	}

	got, ok, err := podB.Get(ctx, gameID)
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	got.Lock()
	st := got.Status
	wk := got.State.WinKind
	got.Unlock()
	if st != StatusFinished {
		t.Fatalf("game must be finished after the cross-pod draw accept, got %s", st)
	}
	if wk != game.WinDraw {
		t.Fatalf("win kind must be WinDraw after accepted draw, got %v", wk)
	}
}
