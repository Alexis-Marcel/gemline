package server

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alexis/gemline/internal/game"
)

// withTestStore returns a Store wired to a noop repo, with a fast clock
// callback that captures forfeit notifications without touching the hub.
func withTestStore(t *testing.T) (*Store, *sync.WaitGroup, *string) {
	t.Helper()
	store := NewStore(nil)
	var wg sync.WaitGroup
	wg.Add(1)
	notified := ""
	store.SetStateListener(func(gameID string) {
		notified = gameID
		wg.Done()
	})
	return store, &wg, &notified
}

func TestClock_ForfeitsActivePlayerWhenTimeExpires(t *testing.T) {
	_ = slog.New(slog.NewTextHandler(io.Discard, nil)) // silence loggers if any
	store, wg, notified := withTestStore(t)
	defer store.Close()

	ctx := context.Background()
	rec, err := store.Create(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Override the clock to make the test fast.
	rec.State.Config.InitialTimeMs = 80
	for i := range rec.State.Players {
		rec.State.Players[i].TimeRemainingMs = 80
	}

	if _, _, err := store.Join(ctx, rec.ID, "Alice", "", -1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Join(ctx, rec.ID, "Bob", "", -1); err != nil {
		t.Fatal(err)
	}

	// Don't play any move. Wait for the timer to fire (~80ms).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("flag listener never fired")
	}

	if *notified != rec.ID {
		t.Fatalf("expected callback for %s, got %q", rec.ID, *notified)
	}

	rec.Lock()
	defer rec.Unlock()
	if rec.Status != StatusFinished {
		t.Errorf("status = %s, want finished", rec.Status)
	}
	if rec.State.WinKind != game.WinTimeout {
		t.Errorf("WinKind = %v, want WinTimeout", rec.State.WinKind)
	}
	if rec.State.Winner != game.C2 {
		t.Errorf("Winner = %v, want C2 (Alice times out as the first player)", rec.State.Winner)
	}
}
