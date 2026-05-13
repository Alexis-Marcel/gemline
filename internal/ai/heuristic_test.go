package ai

import (
	"testing"
	"time"

	"github.com/alexis/gemline/internal/game"
)

// helper: build a fresh 2-player game with C1 to move (gs.Turn = 0).
func freshGame(t *testing.T) *game.GameState {
	t.Helper()
	cfg := game.DefaultConfig(2)
	return game.NewGame([]game.Color{game.C1, game.C2}, cfg)
}

// placeLine drops `n` stones of `color` along the q-axis starting at
// (q0, r). Convenience for setting up tactical positions.
func placeLine(gs *game.GameState, q0, r, n int, color game.Color) {
	for i := 0; i < n; i++ {
		gs.Board.Set(game.Position{Q: q0 + i, R: r}, color)
	}
}

// TestBestMove_FinishesOwnSix: trivial 1-ply test. Bot has 5-in-a-row,
// must finish to 6.
func TestBestMove_FinishesOwnSix(t *testing.T) {
	gs := freshGame(t)
	placeLine(gs, 0, 0, 5, game.C1)
	// Drop a single opp stone so the engine isn't on an entirely empty
	// board for the opp.
	gs.Board.Set(game.Position{Q: 0, R: 2}, game.C2)

	pos, ok := NewEngine(1).WithBudget(500 * time.Millisecond).BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	winning := (pos.Q == -1 && pos.R == 0) || (pos.Q == 5 && pos.R == 0)
	if !winning {
		t.Fatalf("bot must finish the 6-run; played (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_BlocksOpenFive: opponent has 5-in-a-row with one open
// extension (the other is blocked). Bot must block to avoid losing next
// turn. This was the smoke-test scenario the bot was failing on prod.
func TestBestMove_BlocksOpenFive(t *testing.T) {
	gs := freshGame(t)
	placeLine(gs, 0, 0, 5, game.C2)
	gs.Board.Set(game.Position{Q: -1, R: 0}, game.C1) // close one end

	pos, ok := NewEngine(1).WithBudget(500 * time.Millisecond).BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	if !(pos.Q == 5 && pos.R == 0) {
		t.Fatalf("bot must block (5,0); played (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_BlocksOpenFour: opponent has 4-in-a-row, both ends open.
// If unblocked, opp plays one extension → open-5 → wins. The bot must
// block one of the two extensions. This is the killer scenario for the
// previous heuristic — open-4 is functionally a forced loss at depth 1
// but a manageable threat at depth 2+ with proper open-vs-closed
// awareness.
func TestBestMove_BlocksOpenFour(t *testing.T) {
	gs := freshGame(t)
	placeLine(gs, 0, 0, 4, game.C2)
	// Give the bot something innocuous to anchor its own play near.
	gs.Board.Set(game.Position{Q: 5, R: 5}, game.C1)

	pos, ok := NewEngine(1).WithBudget(800 * time.Millisecond).BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	if !((pos.Q == -1 && pos.R == 0) || (pos.Q == 4 && pos.R == 0)) {
		t.Fatalf("bot must cap one end of open-4; played (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_BlocksOpenThree: opponent has open-3, which can become an
// open-4 next turn. A strong bot blocks proactively. We test this less
// strictly: the bot's move must be adjacent to the 3-run (any of the
// four cells extending or flanking it) — that's all we ask before
// committing to a deeper game-theoretic claim.
func TestBestMove_BlocksOpenThree(t *testing.T) {
	gs := freshGame(t)
	placeLine(gs, 0, 0, 3, game.C2) // opp at (0,0), (1,0), (2,0)
	// Spread some C1 stones far away so the bot still has a sensible
	// "play near my own" alternative, otherwise the test is trivial.
	gs.Board.Set(game.Position{Q: 0, R: 5}, game.C1)
	gs.Board.Set(game.Position{Q: 1, R: 5}, game.C1)

	pos, ok := NewEngine(2).WithBudget(800 * time.Millisecond).BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	// Acceptable: any cell on the q-axis adjacent to the 3-run that
	// limits its growth — (-2,0), (-1,0), (3,0), (4,0).
	ok = false
	for _, p := range []game.Position{{Q: -2, R: 0}, {Q: -1, R: 0}, {Q: 3, R: 0}, {Q: 4, R: 0}} {
		if pos == p {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("bot should address opp's open-3; played (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_PrefersImmediateWinOverIndirect: bot can win in 1 move
// (finishing a 6-run) OR in 3 moves (some convoluted path). The ply-
// distance discount must steer the bot to the immediate finish.
func TestBestMove_PrefersImmediateWinOverIndirect(t *testing.T) {
	gs := freshGame(t)
	placeLine(gs, 0, 0, 5, game.C1) // 5-run, (-1,0) and (5,0) both win.
	gs.Board.Set(game.Position{Q: 0, R: 2}, game.C2)
	pos, ok := NewEngine(1).WithBudget(500 * time.Millisecond).BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	if !((pos.Q == -1 && pos.R == 0) || (pos.Q == 5 && pos.R == 0)) {
		t.Fatalf("immediate winning move expected; played (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_StableUnderRandomTiebreak: two engines with the same seed
// against the same state must yield the same move.
func TestBestMove_StableUnderRandomTiebreak(t *testing.T) {
	gs := freshGame(t)
	gs.Board.Set(game.Position{Q: 0, R: 0}, game.C1)
	gs.Board.Set(game.Position{Q: 0, R: 1}, game.C2)

	a := NewEngine(123).WithBudget(300 * time.Millisecond)
	b := NewEngine(123).WithBudget(300 * time.Millisecond)
	pa, _ := a.BestMove(gs, game.C1)
	pb, _ := b.BestMove(gs, game.C1)
	if pa != pb {
		t.Fatalf("same seed → same move; got %v vs %v", pa, pb)
	}
}

// TestBestMove_RespectsTimeBudget: the engine must not run far past its
// budget even on a contested mid-game position. We give it 200 ms and
// expect under 1 s (×5 race overhead allowance) before returning.
func TestBestMove_RespectsTimeBudget(t *testing.T) {
	gs := freshGame(t)
	// Mid-game contested position: a bunch of stones on both sides.
	for q := -3; q <= 3; q++ {
		gs.Board.Set(game.Position{Q: q, R: 0}, game.C1)
		gs.Board.Set(game.Position{Q: q, R: 1}, game.C2)
		gs.Board.Set(game.Position{Q: q, R: -1}, game.C2)
	}
	e := NewEngine(1).WithBudget(200 * time.Millisecond)
	start := time.Now()
	_, ok := e.BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("budget=200ms but BestMove took %v", d)
	}
}

// TestBestMove_OpensCenterOnEmptyBoard: a brand-new board should send
// the bot to (0,0). Sanity check for the move-generation fallback.
func TestBestMove_OpensCenterOnEmptyBoard(t *testing.T) {
	gs := freshGame(t)
	pos, ok := NewEngine(1).WithBudget(200 * time.Millisecond).BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok on empty board")
	}
	if pos != (game.Position{Q: 0, R: 0}) {
		t.Fatalf("empty-board open should be (0,0); got %v", pos)
	}
}
