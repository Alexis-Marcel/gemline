package server

import (
	"context"
	"testing"
	"time"

	"github.com/alexis/gemline/internal/game"
)

// presenceTestStore returns a Store with a tiny disconnect-grace so the
// timeout path runs in milliseconds. We exercise the presence layer at the
// Store level (not via WS) so we don't have to fake a full WebSocket
// connection.
func presenceTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(nil).
		WithBotDelay(0).
		WithDisconnectGrace(20 * time.Millisecond)
}

// waitForStatus polls until the record reaches `want`, or fails after
// `budget`. Used to assert on the asynchronous outcome of a presence-grace
// or clock-flag timer.
func waitForStatus(t *testing.T, rec *GameRecord, want Status, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		rec.Lock()
		got := rec.Status
		rec.Unlock()
		if got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	rec.Lock()
	got := rec.Status
	rec.Unlock()
	t.Fatalf("status %q did not become %q within %v", got, want, budget)
}

// playableTwoPlayer fills both seats and starts the game so seatRefs and
// presence tracking have something real to attach to.
func playableTwoPlayer(t *testing.T, s *Store) (*GameRecord, *Seat, *Seat) {
	t.Helper()
	ctx := context.Background()
	rec, err := s.Create(ctx, 2, VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.Join(ctx, rec.ID, "Alice", "", -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Join(ctx, rec.ID, "Bob", "", -1)
	if err != nil {
		t.Fatal(err)
	}
	rec.Lock()
	st := rec.Status
	rec.Unlock()
	if st != StatusPlaying {
		t.Fatalf("setup: want playing, got %s", st)
	}
	return rec, a, b
}

func TestPresence_DisconnectGraceForfeitsAfterTimeout(t *testing.T) {
	s := presenceTestStore(t)
	rec, a, _ := playableTwoPlayer(t, s)

	s.SeatConnected(rec.ID, a.Index)
	s.SeatDisconnected(rec.ID, a.Index)

	waitForStatus(t, rec, StatusFinished, 1*time.Second)

	rec.Lock()
	wk := rec.State.WinKind
	winner := rec.State.Winner
	rec.Unlock()
	if wk != game.WinTimeout {
		t.Fatalf("want WinTimeout for the disconnect forfeit, got %v", wk)
	}
	if winner == game.Empty {
		t.Fatalf("2-player disconnect forfeit must declare the survivor; got Empty")
	}

	// gameEnded must have cleared the seatRefs entry so long-running
	// servers don't leak one map per finished game.
	s.mu.Lock()
	_, present := s.seatRefs[rec.ID]
	s.mu.Unlock()
	if present {
		t.Fatalf("seatRefs[%s] should be cleared after gameEnded", rec.ID)
	}
}

func TestPresence_ReconnectCancelsGrace(t *testing.T) {
	s := presenceTestStore(t)
	rec, a, _ := playableTwoPlayer(t, s)

	s.SeatConnected(rec.ID, a.Index)
	s.SeatDisconnected(rec.ID, a.Index)
	time.Sleep(5 * time.Millisecond)
	s.SeatConnected(rec.ID, a.Index)

	time.Sleep(60 * time.Millisecond)
	rec.Lock()
	st := rec.Status
	rec.Unlock()
	if st != StatusPlaying {
		t.Fatalf("reconnect within grace should keep us in play, status=%s", st)
	}
}

func TestPresence_RefcountedConnectionsTreatLastDisconnectAsOffline(t *testing.T) {
	s := presenceTestStore(t)
	rec, a, _ := playableTwoPlayer(t, s)

	s.SeatConnected(rec.ID, a.Index)
	s.SeatConnected(rec.ID, a.Index)
	s.SeatDisconnected(rec.ID, a.Index) // one tab gone, one still open

	time.Sleep(50 * time.Millisecond)
	rec.Lock()
	st := rec.Status
	rec.Unlock()
	if st != StatusPlaying {
		t.Fatalf("first disconnect with second tab open shouldn't forfeit; status=%s", st)
	}

	s.SeatDisconnected(rec.ID, a.Index)
	waitForStatus(t, rec, StatusFinished, 1*time.Second)
}
