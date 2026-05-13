// Package ai exposes a lightweight, single-strength heuristic engine for
// computer-controlled players. The aim is not "strong play" — it is "playable
// solo practice that won't fall over". The engine evaluates every legal move
// by cloning the board, simulating the placement (including resulting
// captures), then scoring the resulting position. Higher score wins; ties
// break randomly so two runs from the same state can diverge slightly.
package ai

import (
	"math/rand"

	"github.com/alexis/gemline/internal/game"
)

// Engine is the public entry point. Holding state (just a PRNG) keeps the
// move selection deterministic for tests by injecting a seeded source, while
// production callers get fresh randomness per construction.
type Engine struct {
	rng *rand.Rand
}

func NewEngine(seed int64) *Engine {
	return &Engine{rng: rand.New(rand.NewSource(seed))}
}

// BestMove returns the move the heuristic prefers for `color`, or (zero, false)
// if there is no legal play (full board, game over, or color isn't on the
// turn). Callers should verify that `state.CurrentPlayer().Color == color`
// before invoking — the engine doesn't enforce turn order itself, it just
// picks the strongest candidate from `color`'s perspective.
func (e *Engine) BestMove(state *game.GameState, color game.Color) (game.Position, bool) {
	if state.IsOver() {
		return game.Position{}, false
	}

	type scored struct {
		pos   game.Position
		score int
	}
	var candidates []scored
	bestScore := minScore

	r := state.Board.Side - 1
	for q := -r; q <= r; q++ {
		for s := -r; s <= r; s++ {
			p := game.Position{Q: q, R: s}
			if !state.Board.In(p) || state.Board.At(p) != game.Empty {
				continue
			}
			score := evaluate(state, p, color)
			if score > bestScore {
				bestScore = score
				candidates = candidates[:0]
				candidates = append(candidates, scored{p, score})
			} else if score == bestScore {
				candidates = append(candidates, scored{p, score})
			}
		}
	}
	if len(candidates) == 0 {
		return game.Position{}, false
	}
	pick := candidates[e.rng.Intn(len(candidates))]
	return pick.pos, true
}

const minScore = -1 << 30

// evaluate scores a candidate placement at `p` by `color`. The components:
//
//   - Immediate-win check: if the placement triggers a 6-alignment or pushes
//     us across the configured alignment / capture threshold, return a near-
//     infinite score so that move wins over anything else.
//   - Captures: each pair captured is worth a lot — both because it advances
//     the capture-pairs win condition and because it removes opponent stones
//     that may have been part of a threatening line.
//   - Defensive: how much do we *break* of the opponent's existing alignments?
//     Approximated by the drop in their (4, 5)-alignment counts after we sit
//     on `p`. This naturally blocks "extension cells" that would have grown
//     a 4-run into a 5-run.
//   - Offensive: the gain in our own (4, 5)-alignment counts.
//   - A small base value drawn from the cell's distance to the center, so
//     when nothing strategic is going on the bot still trends toward the
//     middle of the board rather than the corners.
func evaluate(state *game.GameState, p game.Position, color game.Color) int {
	// Snapshot the live counters we'll diff against post-move.
	oppColor := nextPlayerColor(state, color)
	mineA5Before := game.CountMaximalRuns(state.Board, color, 5)
	mineA4Before := game.CountMaximalRuns(state.Board, color, 4)
	oppA5Before := game.CountMaximalRuns(state.Board, oppColor, 5)
	oppA4Before := game.CountMaximalRuns(state.Board, oppColor, 4)

	// Clone-simulate the placement so we can measure the resulting position
	// without disturbing the engine's authoritative state.
	sim := state.Board.Clone()
	sim.Set(p, color)
	captures := game.DetectCaptures(sim, p, color)
	for _, c := range captures {
		sim.Set(c.Pair[0], game.Empty)
		sim.Set(c.Pair[1], game.Empty)
	}

	// Win detection — both the absolute-victory 6-run and the
	// threshold-driven 5/4-runs and capture counts. The capture count is
	// the player's existing tally plus pairs captured *by this move*.
	if game.HasRun(sim, color, 6) {
		return winScore
	}
	mineA5After := game.CountMaximalRuns(sim, color, 5)
	mineA4After := game.CountMaximalRuns(sim, color, 4)
	cfg := state.Config
	if cfg.Align5ToWin > 0 && mineA5After >= cfg.Align5ToWin {
		return winScore - 1
	}
	if cfg.Align4ToWin > 0 && mineA4After >= cfg.Align4ToWin {
		return winScore - 2
	}
	mePlayer := playerByColor(state, color)
	if mePlayer != nil && cfg.CapturePairsWin > 0 &&
		mePlayer.CapturedPairs+len(captures) >= cfg.CapturePairsWin {
		return winScore - 3
	}

	// Defensive: does this move (or its captures) shrink the opponent's
	// alignment counts? Higher weights for breaking longer chains.
	oppA5After := game.CountMaximalRuns(sim, oppColor, 5)
	oppA4After := game.CountMaximalRuns(sim, oppColor, 4)
	defense := (oppA5Before-oppA5After)*240 + (oppA4Before-oppA4After)*60

	// Offensive: own alignment growth.
	offense := (mineA5After-mineA5Before)*120 + (mineA4After-mineA4Before)*30

	// Capture value: each pair is concrete progress + tempo.
	captureScore := len(captures) * 80

	// Center bias: 11-side board, max axial distance is 10. Closer = +,
	// keeps the bot from wandering to the rim when nothing else matters.
	center := 10 - hexDistance(p, game.Position{})

	return defense + offense + captureScore + center
}

const winScore = 1_000_000

// nextPlayerColor returns the color of whoever would play after `color`'s
// turn. Used to estimate "what threats would the opponent have left?"
// without simulating an entire opponent move on top.
func nextPlayerColor(state *game.GameState, color game.Color) game.Color {
	// Find color's index, return next.
	for i, p := range state.Players {
		if p.Color == color {
			return state.Players[(i+1)%len(state.Players)].Color
		}
	}
	return game.Empty
}

func playerByColor(state *game.GameState, color game.Color) *game.Player {
	for i := range state.Players {
		if state.Players[i].Color == color {
			return &state.Players[i]
		}
	}
	return nil
}

// hexDistance is the axial-coordinate distance on a hex grid. Used only by
// the centre-bias tie-breaker; we don't need full hex geometry here.
func hexDistance(a, b game.Position) int {
	dq := a.Q - b.Q
	dr := a.R - b.R
	ds := -dq - dr
	if dq < 0 {
		dq = -dq
	}
	if dr < 0 {
		dr = -dr
	}
	if ds < 0 {
		ds = -ds
	}
	if dq >= dr && dq >= ds {
		return dq
	}
	if dr >= ds {
		return dr
	}
	return ds
}
