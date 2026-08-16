package game

import (
	"testing"
	"time"
)

// ---- ConfigFor ----

func TestConfigFor_PreservesTimeControl(t *testing.T) {
	base := Config{InitialTimeMs: 5 * 60 * 1000, IncrementMs: 2000}
	cfg := ConfigFor(4, base)
	if cfg.InitialTimeMs != base.InitialTimeMs || cfg.IncrementMs != base.IncrementMs {
		t.Errorf("time control not carried through: got %d/%d", cfg.InitialTimeMs, cfg.IncrementMs)
	}
	want := DefaultConfig(4)
	if cfg.CapturePairsWin != want.CapturePairsWin || cfg.Align4ToWin != want.Align4ToWin || cfg.Align5ToWin != want.Align5ToWin {
		t.Errorf("thresholds = %+v, want those of DefaultConfig(4)", cfg)
	}
	if cfg.BoardSide != DefaultBoardSide {
		t.Errorf("BoardSide = %d, want default %d when base has none", cfg.BoardSide, DefaultBoardSide)
	}
}

func TestConfigFor_PreservesCustomBoardSide(t *testing.T) {
	cfg := ConfigFor(3, Config{BoardSide: 7})
	if cfg.BoardSide != 7 {
		t.Errorf("BoardSide = %d, want 7", cfg.BoardSide)
	}
}

// ---- Clone ----

func TestClone_IsDeepAndOmitsHistory(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(0, 0)}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	c := g.Clone()
	if c.History != nil {
		t.Errorf("clone should omit history, got %d moves", len(c.History))
	}
	if c.Board.At(pos(0, 0)) != C1 || c.Turn != g.Turn {
		t.Fatalf("clone did not copy board/turn state")
	}

	c.Board.Set(pos(1, 1), C2)
	c.Players[0].GemsRemaining = 0
	if g.Board.At(pos(1, 1)) != Empty {
		t.Errorf("mutating the clone's board leaked into the original")
	}
	if g.Players[0].GemsRemaining == 0 {
		t.Errorf("mutating the clone's players leaked into the original")
	}
}

// ---- RemainingForActive ----

func TestRemainingForActive(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("clock disabled returns stored value", func(t *testing.T) {
		g := NewGame([]Color{C1, C2}, Config{BoardSide: 11})
		if got := g.RemainingForActive(base); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("before StartClock returns full budget", func(t *testing.T) {
		g := NewGame([]Color{C1, C2}, DefaultConfig(2))
		if got := g.RemainingForActive(base); got != DefaultInitialTimeMs {
			t.Errorf("got %d, want %d", got, DefaultInitialTimeMs)
		}
	})

	t.Run("deducts elapsed time", func(t *testing.T) {
		g := NewGame([]Color{C1, C2}, DefaultConfig(2))
		g.StartClock(base)
		got := g.RemainingForActive(base.Add(3 * time.Second))
		if want := DefaultInitialTimeMs - 3000; got != want {
			t.Errorf("got %d, want %d", got, want)
		}
	})

	t.Run("clamps at zero", func(t *testing.T) {
		g := NewGame([]Color{C1, C2}, DefaultConfig(2))
		g.StartClock(base)
		if got := g.RemainingForActive(base.Add(time.Hour)); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("frozen once the game is over", func(t *testing.T) {
		g := NewGame([]Color{C1, C2}, DefaultConfig(2))
		g.StartClock(base)
		g.Resign(C1)
		if got := g.RemainingForActive(base.Add(time.Hour)); got != DefaultInitialTimeMs {
			t.Errorf("got %d, want untouched %d", got, DefaultInitialTimeMs)
		}
	})
}

func TestApplyMove_ToleratesClockSkew(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.StartClock(base)
	// `now` earlier than TurnStartedAt must not credit time back.
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(0, 0)}, base.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := g.Players[0].TimeRemainingMs; got != DefaultInitialTimeMs {
		t.Errorf("TimeRemainingMs = %d, want unchanged %d", got, DefaultInitialTimeMs)
	}
}

// ---- Resign / AgreeDraw / Forfeit endings ----

func TestResign_TwoPlayer(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.Resign(C1)
	if g.WinKind != WinResign || g.Winner != C2 {
		t.Errorf("got winner=%v kind=%v, want C2/WinResign", g.Winner, g.WinKind)
	}
	if !g.IsOver() {
		t.Errorf("IsOver should be true after resign")
	}
}

func TestResign_MultiplayerHasNoWinner(t *testing.T) {
	g := NewGame([]Color{C1, C2, C3}, DefaultConfig(3))
	g.Resign(C2)
	if g.WinKind != WinResign || g.Winner != Empty {
		t.Errorf("got winner=%v kind=%v, want Empty/WinResign", g.Winner, g.WinKind)
	}
}

func TestResign_NoOpOnFinishedGame(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.Winner = C1
	g.WinKind = WinAlignment6
	g.Resign(C1)
	if g.Winner != C1 || g.WinKind != WinAlignment6 {
		t.Fatalf("Resign overwrote a finished game: winner=%v kind=%v", g.Winner, g.WinKind)
	}
}

func TestAgreeDraw(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.AgreeDraw()
	if g.WinKind != WinDraw || g.Winner != Empty {
		t.Errorf("got winner=%v kind=%v, want Empty/WinDraw", g.Winner, g.WinKind)
	}
	if !g.IsOver() {
		t.Errorf("IsOver should be true after a draw")
	}
}

func TestAgreeDraw_NoOpOnFinishedGame(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.Resign(C2)
	g.AgreeDraw()
	if g.WinKind != WinResign {
		t.Fatalf("AgreeDraw overwrote a finished game: kind=%v", g.WinKind)
	}
}

func TestForfeit_MultiplayerZeroesLoserClock(t *testing.T) {
	g := NewGame([]Color{C1, C2, C3}, DefaultConfig(3))
	g.Forfeit(C2)
	if g.WinKind != WinTimeout || g.Winner != Empty {
		t.Errorf("got winner=%v kind=%v, want Empty/WinTimeout", g.Winner, g.WinKind)
	}
	if g.Players[1].TimeRemainingMs != 0 {
		t.Errorf("loser's clock = %d, want 0", g.Players[1].TimeRemainingMs)
	}
	if g.Players[0].TimeRemainingMs == 0 || g.Players[2].TimeRemainingMs == 0 {
		t.Errorf("survivors' clocks must not be zeroed")
	}
}

// ---- String forms ----

func TestColor_String_Edges(t *testing.T) {
	cases := map[Color]string{OffBoard: " ", Empty: ".", C1: "1", C6: "6", Color(9): "?"}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Color(%d).String() = %q, want %q", c, got, want)
		}
	}
}

func TestWinKind_String_Edges(t *testing.T) {
	cases := map[WinKind]string{WinTimeout: "timeout", WinResign: "resign", WinDraw: "draw", WinKind(42): "?"}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("WinKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// ---- Capture guards ----

func TestCapture_NoneWhenFlankerOffBoard(t *testing.T) {
	g := NewGame([]Color{C1, C2}, Config{BoardSide: 11})
	// Pattern pointing off the east edge: the far flanker slot doesn't exist.
	g.Board.Set(pos(9, 0), C2)
	g.Board.Set(pos(10, 0), C2)
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(8, 0)}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Captures) != 0 {
		t.Fatalf("want no capture at the board edge, got %d", len(res.Captures))
	}
}

func TestCapture_NoneOnEmptyMiddle(t *testing.T) {
	g := NewGame([]Color{C1, C2}, Config{BoardSide: 11})
	// [C1][empty][C2][placed C1] — a hole in the sandwich is not a capture.
	g.Board.Set(pos(-2, 0), C1)
	g.Board.Set(pos(0, 0), C2)
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Captures) != 0 {
		t.Fatalf("want no capture with an empty middle, got %d", len(res.Captures))
	}
}
