// Package ai implements the computer opponent for Gemline.
//
// Architecture:
//   - Pattern-based static evaluator (see threatValue). For each colour
//     we walk every maximal run on every axis, weigh it by length AND
//     how many of its endpoints are still extendable. Open patterns
//     (both ends free) get exponentially more weight than closed ones —
//     an open-4 is a one-move-from-win threat, a closed-4 is mostly
//     historical.
//   - Negamax with α-β pruning over an iterative-deepening loop. Each
//     deepening pass uses the previous pass's best move as the first
//     candidate, which dramatically improves α-β cutoffs.
//   - Smart move generation: empty cells within distance 2 of any stone,
//     ranked by a cheap shallow score. Anchored at the board centre when
//     the board is empty, so the bot never wanders to a corner on its
//     opening move.
//   - Time budget (default 600 ms) caps the deepening loop so the bot
//     responds quickly even when the search would have gone deeper.
package ai

import (
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/alexis/gemline/internal/game"
)

const debug = false

// Engine is the bot's public entry point. Holding state (the PRNG +
// time budget) makes its behaviour deterministic for tests but
// configurable for prod.
type Engine struct {
	rng    *rand.Rand
	budget time.Duration
}

// NewEngine returns an engine seeded for tiebreak randomness. The default
// per-move budget is 600 ms — comfortable for production responsiveness
// while still allowing depth-4 search on mid-game positions.
func NewEngine(seed int64) *Engine {
	return &Engine{
		rng:    rand.New(rand.NewSource(seed)),
		budget: 600 * time.Millisecond,
	}
}

// WithBudget overrides the per-move thinking time. Tests use it to keep
// the search predictable; production uses the default.
func (e *Engine) WithBudget(d time.Duration) *Engine {
	e.budget = d
	return e
}

const (
	winScore = 1_000_000_000
	minScore = -1 << 30
	maxDepth = 6

	// Move-generation distance: only consider empty cells within 2 hex
	// steps of an existing stone. On an 11-side board that keeps the
	// search frontier focused on where action is actually happening.
	moveRadius = 2

	// Per-ply candidate cap. Even with smart generation a mid-game
	// position can offer ~80 candidates; sorting by shallow score and
	// truncating to topCandidates focuses the search on the most
	// promising branches first (helping α-β cutoffs).
	topCandidates = 24
)

// BestMove returns the move the engine prefers for `color`, or
// (zero, false) when there's no legal play.
func (e *Engine) BestMove(state *game.GameState, color game.Color) (game.Position, bool) {
	if state.IsOver() {
		return game.Position{}, false
	}
	deadline := time.Now().Add(e.budget)

	candidates := generateMoves(state, color)
	if len(candidates) == 0 {
		return game.Position{}, false
	}

	// Iterative deepening. At each depth we re-order candidates so the
	// best move from the previous pass is searched first; that's where
	// α-β gets its leverage.
	var (
		bestSoFar []game.Position
		bestScore int
	)
	for depth := 1; depth <= maxDepth; depth++ {
		picks, score, completed := e.searchRoot(state, color, candidates, depth, deadline)
		if !completed && len(bestSoFar) > 0 {
			// Time ran out mid-pass; keep the previous depth's result.
			break
		}
		if debug {
			fmt.Printf("depth=%d completed=%v score=%d picks=%v\n", depth, completed, score, picks)
		}
		bestSoFar = picks
		bestScore = score
		// If we've found a forced mate, no point searching deeper.
		if bestScore >= winScore-1000 || bestScore <= -winScore+1000 {
			break
		}
		// Reorder candidates: best from this pass first, then the rest.
		candidates = promoteBest(candidates, picks[0])
		if time.Now().After(deadline) {
			break
		}
	}

	if len(bestSoFar) == 0 {
		return game.Position{}, false
	}
	return bestSoFar[e.rng.Intn(len(bestSoFar))], true
}

// searchRoot runs a single iterative-deepening pass at `depth`. Returns
// the best moves (ties preserved for randomness), their score, and
// `completed=false` if the deadline elapsed mid-pass.
func (e *Engine) searchRoot(state *game.GameState, color game.Color, candidates []game.Position, depth int, deadline time.Time) ([]game.Position, int, bool) {
	alpha, beta := minScore, -minScore
	bestScore := minScore
	var bestMoves []game.Position
	for _, pos := range candidates {
		if time.Now().After(deadline) {
			return bestMoves, bestScore, false
		}
		child := state.Clone()
		if _, err := child.ApplyMove(game.Move{Player: color, Pos: pos}, child.TurnStartedAt); err != nil {
			continue
		}
		score := -negamax(child, opponentOf(child, color), depth-1, -beta, -alpha, deadline)
		score = discountTerminal(score)
		if score > bestScore {
			bestScore = score
			bestMoves = append(bestMoves[:0], pos)
			if score > alpha {
				alpha = score
			}
		} else if score == bestScore {
			bestMoves = append(bestMoves, pos)
		}
	}
	return bestMoves, bestScore, true
}

func negamax(state *game.GameState, mover game.Color, depth, alpha, beta int, deadline time.Time) int {
	if state.IsOver() {
		return terminalScore(state, mover)
	}
	if depth == 0 {
		return evaluate(state, mover)
	}
	if time.Now().After(deadline) {
		return evaluate(state, mover)
	}

	candidates := generateMoves(state, mover)
	if len(candidates) == 0 {
		return evaluate(state, mover)
	}

	best := minScore
	for _, pos := range candidates {
		child := state.Clone()
		if _, err := child.ApplyMove(game.Move{Player: mover, Pos: pos}, child.TurnStartedAt); err != nil {
			continue
		}
		score := -negamax(child, opponentOf(child, mover), depth-1, -beta, -alpha, deadline)
		score = discountTerminal(score)
		if score > best {
			best = score
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			break
		}
	}
	return best
}

// discountTerminal nudges near-win/near-loss scores by 1 on each
// recursion level so callers prefer "win sooner / lose later" all else
// equal. The threshold is generous (within 1000 of ±winScore) so any
// truly tactical value crosses it.
func discountTerminal(score int) int {
	switch {
	case score >= winScore-1000:
		return score - 1
	case score <= -winScore+1000:
		return score + 1
	}
	return score
}

func terminalScore(state *game.GameState, perspective game.Color) int {
	switch state.Winner {
	case perspective:
		return winScore
	case game.Empty:
		return 0
	default:
		return -winScore
	}
}

// evaluate is the static board evaluation from `perspective`'s point of
// view. Sums pattern values for own and opponent runs (open vs closed
// awareness) and captures, returns the net.
func evaluate(state *game.GameState, perspective game.Color) int {
	me := threatValue(state.Board, perspective)
	opp := 0
	for _, p := range state.Players {
		if p.Color == perspective {
			continue
		}
		opp += threatValue(state.Board, p.Color)
	}
	mePlayer := playerByColor(state, perspective)
	myCaps := 0
	if mePlayer != nil {
		myCaps = mePlayer.CapturedPairs
	}
	oppCaps := 0
	for _, p := range state.Players {
		if p.Color != perspective {
			oppCaps += p.CapturedPairs
		}
	}
	cfg := state.Config
	// Captures matter more as the win threshold approaches.
	captureWeight := 60
	if cfg.CapturePairsWin > 0 {
		captureWeight += 8 * cfg.CapturePairsWin / max(cfg.CapturePairsWin-myCaps, 1)
	}
	return me - opp + captureWeight*(myCaps-oppCaps)
}

// threatValue scores every maximal run of `color` on the board, with
// length and open-end count driving the weight. The values are tuned to
// roughly match Pente/Gomoku heuristics:
//
//	open-3:  300        — can become open-4 next turn
//	open-4:  4000       — opponent must block or lose
//	open-5:  100000     — one move from a 6-win; nearly forced terminal
//	closed-3: 30
//	closed-4: 400
//	closed-5: 8000      — still a threat (the open end can finish 6)
//
// 6-runs are caught by the terminal check (Winner is set the moment a
// 6-alignment appears), so they don't need a special pattern here.
func threatValue(b *game.Board, color game.Color) int {
	total := 0
	r := b.Side - 1
	for q := -r; q <= r; q++ {
		for s := -r; s <= r; s++ {
			p := game.Position{Q: q, R: s}
			if !b.In(p) || b.At(p) != color {
				continue
			}
			for _, d := range game.Directions {
				// Skip if this cell isn't the start of a maximal run in
				// direction d — only count each run once.
				prev := game.Position{Q: p.Q - d.Q, R: p.R - d.R}
				if b.In(prev) && b.At(prev) == color {
					continue
				}
				length := 0
				end := p
				for b.In(end) && b.At(end) == color {
					length++
					end = game.Position{Q: end.Q + d.Q, R: end.R + d.R}
				}
				if length < 2 {
					continue
				}
				openEnds := 0
				if b.In(prev) && b.At(prev) == game.Empty {
					openEnds++
				}
				if b.In(end) && b.At(end) == game.Empty {
					openEnds++
				}
				total += runScore(length, openEnds)
			}
		}
	}
	return total
}

// runScore is the weight table referenced from threatValue. Encoded as
// switch rather than a 2D array for readability.
func runScore(length, openEnds int) int {
	if length >= 5 {
		// 5-runs are nearly terminal. With at least one open end, the
		// player can finish the 6-alignment on the next move.
		switch openEnds {
		case 0:
			return 0
		case 1:
			return 8_000
		default:
			return 100_000
		}
	}
	switch length {
	case 4:
		switch openEnds {
		case 0:
			return 0
			// Closed-4 with one open end can become a 5-run; not as
			// devastating as open-4 but still significant.
		case 1:
			return 400
		default:
			return 4_000
		}
	case 3:
		switch openEnds {
		case 0:
			return 0
		case 1:
			return 30
		default:
			return 300
		}
	case 2:
		switch openEnds {
		case 0:
			return 0
		case 1:
			return 4
		default:
			return 20
		}
	}
	return 0
}

// generateMoves returns all empty cells that are reasonable candidates:
// within `moveRadius` of an existing stone (because Gemline moves
// influence neighbours, not far-away corners). Sorted by a cheap shallow
// score so the search explores high-value branches first.
//
// On a completely empty board, falls back to the centre.
func generateMoves(state *game.GameState, mover game.Color) []game.Position {
	b := state.Board
	r := b.Side - 1
	hasAny := false
	for q := -r; q <= r; q++ {
		for s := -r; s <= r; s++ {
			p := game.Position{Q: q, R: s}
			if b.In(p) && b.At(p) != game.Empty && b.At(p) != game.OffBoard {
				hasAny = true
				break
			}
		}
		if hasAny {
			break
		}
	}
	if !hasAny {
		// Empty board → play the centre.
		return []game.Position{{Q: 0, R: 0}}
	}

	type scored struct {
		pos   game.Position
		score int
	}
	var pool []scored
	for q := -r; q <= r; q++ {
		for s := -r; s <= r; s++ {
			p := game.Position{Q: q, R: s}
			if !b.In(p) || b.At(p) != game.Empty {
				continue
			}
			if !hasStoneNearby(b, p, moveRadius) {
				continue
			}
			pool = append(pool, scored{p, shallowMoveScore(state, p, mover)})
		}
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].score > pool[j].score })
	if len(pool) > topCandidates {
		pool = pool[:topCandidates]
	}
	out := make([]game.Position, len(pool))
	for i, p := range pool {
		out[i] = p.pos
	}
	return out
}

// promoteBest moves `pv` (the best move from a previous deepening pass)
// to the front of `candidates`, preserving the rest of the order.
// Improves α-β cutoffs on the next deeper pass.
func promoteBest(candidates []game.Position, pv game.Position) []game.Position {
	for i, c := range candidates {
		if c == pv {
			if i == 0 {
				return candidates
			}
			out := make([]game.Position, len(candidates))
			out[0] = pv
			copy(out[1:i+1], candidates[:i])
			copy(out[i+1:], candidates[i+1:])
			return out
		}
	}
	return candidates
}

// hasStoneNearby reports whether any of the cells within axial distance
// `radius` of `p` (exclusive of p itself) currently holds a stone of any
// playing colour.
func hasStoneNearby(b *game.Board, p game.Position, radius int) bool {
	for dq := -radius; dq <= radius; dq++ {
		for dr := -radius; dr <= radius; dr++ {
			if dq == 0 && dr == 0 {
				continue
			}
			if abs(dq+dr) > radius {
				continue
			}
			q := game.Position{Q: p.Q + dq, R: p.R + dr}
			if !b.In(q) {
				continue
			}
			c := b.At(q)
			if c != game.Empty && c != game.OffBoard {
				return true
			}
		}
	}
	return false
}

// shallowMoveScore is the 1-ply quick eval used to order candidates for
// the search. It plays the candidate on a cloned board, runs threatValue
// for both sides on the resulting position, and reports the net change.
// This sees captures and pattern shifts in one move's worth of work.
//
// Crucially, moves that immediately *win* the game (or block the
// opponent's immediate win) get pinned to the top of the ordering with
// a score near ±winScore. Without that the search's top-K candidate
// filter could quietly drop a winning move whose threatValue delta
// looks like any other extension.
func shallowMoveScore(state *game.GameState, p game.Position, color game.Color) int {
	oppColor := opponentOf(state, color)
	// Pre-move totals.
	beforeMe := threatValue(state.Board, color)
	beforeOpp := threatValue(state.Board, oppColor)

	sim := state.Board.Clone()
	sim.Set(p, color)
	// Captures triggered by placing here.
	captures := game.DetectCaptures(sim, p, color)
	for _, c := range captures {
		sim.Set(c.Pair[0], game.Empty)
		sim.Set(c.Pair[1], game.Empty)
	}

	// Immediate-win detection: 6-alignment, threshold-driven 5/4-runs,
	// or capture-pair threshold. Any of these pins this candidate to
	// the top of the ordering so the search can't lose it to the
	// top-K filter.
	cfg := state.Config
	if game.HasRun(sim, color, 6) {
		return winScore
	}
	if cfg.Align5ToWin > 0 && game.CountMaximalRuns(sim, color, 5) >= cfg.Align5ToWin {
		return winScore - 1
	}
	if cfg.Align4ToWin > 0 && game.CountMaximalRuns(sim, color, 4) >= cfg.Align4ToWin {
		return winScore - 2
	}
	if cfg.CapturePairsWin > 0 {
		mePlayer := playerByColor(state, color)
		myCaps := 0
		if mePlayer != nil {
			myCaps = mePlayer.CapturedPairs
		}
		if myCaps+len(captures) >= cfg.CapturePairsWin {
			return winScore - 3
		}
	}

	afterMe := threatValue(sim, color)
	afterOpp := threatValue(sim, oppColor)

	gain := (afterMe - beforeMe) - (afterOpp - beforeOpp)
	gain += len(captures) * 200 // each captured pair is concrete progress
	// Bias toward the centre as a tiebreaker — far-off cells with no
	// pattern impact otherwise look identical to one another.
	gain += 10 - hexDistance(p, game.Position{})
	return gain
}

func opponentOf(state *game.GameState, color game.Color) game.Color {
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

func hexDistance(a, b game.Position) int {
	dq := a.Q - b.Q
	dr := a.R - b.R
	ds := -dq - dr
	return (abs(dq) + abs(dr) + abs(ds)) / 2
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
