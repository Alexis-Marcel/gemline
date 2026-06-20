package ai

import (
	"testing"
	"time"

	"github.com/alexis-marcel/gemline/internal/game"
)

// TestBestMove_TakesObviousWin: with a 5-line and a cell that completes a
// 6-alignment, the bot must take it.
func TestBestMove_TakesObviousWin(t *testing.T) {
	cfg := game.DefaultConfig(2)
	gs := game.NewGame([]game.Color{game.C1, game.C2}, cfg)
	for q := 0; q < 5; q++ {
		gs.Board.Set(game.Position{Q: q, R: 0}, game.C1)
	}
	gs.Board.Set(game.Position{Q: 0, R: 1}, game.C2)

	e := NewEngine(42)
	pos, ok := e.BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok on a winning position")
	}
	if !(pos.Q == -1 && pos.R == 0) && !(pos.Q == 5 && pos.R == 0) {
		t.Fatalf("expected the winning cell at (-1,0) or (5,0), got (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_AvoidsImmediateLoss: a 1-ply bot prefers extending its own
// 4-run over blocking the opponent's 5-run (CountMaximalRuns doesn't drop when
// blocking a tail); the 2-ply bot sees the opponent's winning reply and blocks.
// One 5-run extension is pre-blocked so exactly one defensive cell remains,
// making it a real block-or-lose choice.
func TestBestMove_AvoidsImmediateLoss(t *testing.T) {
	cfg := game.DefaultConfig(2)
	gs := game.NewGame([]game.Color{game.C1, game.C2}, cfg)
	// C2's 5-run with (-1,0) pre-blocked by C1, so only (5,0) extends.
	gs.Board.Set(game.Position{Q: -1, R: 0}, game.C1)
	for q := 0; q <= 4; q++ {
		gs.Board.Set(game.Position{Q: q, R: 0}, game.C2)
	}
	// C1's own 4-run, away from C2's threat.
	for q := 0; q <= 3; q++ {
		gs.Board.Set(game.Position{Q: q, R: 4}, game.C1)
	}

	e := NewEngine(42)
	pos, ok := e.BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	if !(pos.Q == 5 && pos.R == 0) {
		t.Fatalf("bot should block C2's only open extension at (5,0); played (%d,%d)", pos.Q, pos.R)
	}
}

// TestBestMove_StableUnderRandomTiebreak: same seed and state must yield the
// same move. Guards against non-determinism (e.g. map ranging).
func TestBestMove_StableUnderRandomTiebreak(t *testing.T) {
	cfg := game.DefaultConfig(2)
	gs := game.NewGame([]game.Color{game.C1, game.C2}, cfg)
	gs.Board.Set(game.Position{Q: 0, R: 0}, game.C1)
	gs.Board.Set(game.Position{Q: 0, R: 1}, game.C2)

	a := NewEngine(123)
	b := NewEngine(123)
	pa, _ := a.BestMove(gs, game.C1)
	pb, _ := b.BestMove(gs, game.C1)
	if pa != pb {
		t.Fatalf("same seed should yield same move; got %v vs %v", pa, pb)
	}
}

// TestBestMove_PerformanceBudget guards against a quadratic blow-up if someone
// widens topCandidatesAtDepth* — not a speed benchmark.
func TestBestMove_PerformanceBudget(t *testing.T) {
	cfg := game.DefaultConfig(2)
	gs := game.NewGame([]game.Color{game.C1, game.C2}, cfg)
	// ~20 stones to make the search non-trivial.
	for q := -2; q <= 2; q++ {
		gs.Board.Set(game.Position{Q: q, R: 0}, game.C1)
		gs.Board.Set(game.Position{Q: q, R: 1}, game.C2)
		gs.Board.Set(game.Position{Q: q, R: -1}, game.C1)
		gs.Board.Set(game.Position{Q: q, R: 2}, game.C2)
	}

	e := NewEngine(42)
	start := time.Now()
	_, ok := e.BestMove(gs, game.C1)
	if !ok {
		t.Fatal("BestMove returned !ok")
	}
	// 3s ceiling (not 500ms) because -race in CI slows the search ~5×.
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("BestMove took %v, expected <3s under -race", d)
	}
}
