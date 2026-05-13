// Package ai exposes a lightweight bot for computer-controlled players.
// The engine runs a depth-2 minimax search (with α-β pruning) over the
// available moves and falls back on a heuristic position scorer at the
// leaves. The goal is "playable solo practice that won't fall over on
// obvious one-move-ahead tactics" — not optimal play.
//
// Two-ply search means the bot considers: "I play M; opponent plays the
// best response N; what does the board look like for me?" That's enough
// to dodge the most embarrassing class of tactical blunder (taking a
// piece that hands the opponent an immediate win, walking into a fork,
// etc.) without paying for deeper search.
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

const (
	// searchDepth = number of plies the bot looks ahead. 1 = only the bot's
	// own move (the old behaviour). 2 = bot's move + opponent's best reply.
	// Higher = stronger but slower; 2 is the sweet spot at the heuristic's
	// current strength.
	searchDepth = 2

	minScore = -1 << 30
	winScore = 1_000_000

	// At the root we trim to the top-N by shallowScore so the search is
	// bounded, but N is intentionally generous. Earlier we tried 18 and
	// silently discarded tactically critical defensive moves whose
	// shallowScore is low (blocking the empty end of an opponent's 5-run
	// doesn't shift CountMaximalRuns, so the static eval undervalues it).
	// 60 is enough to catch those plus a healthy margin of "junk" moves
	// that turn out to score well under minimax.
	//
	// Opponent's responses (depth 1) still get a smaller cap — the bot
	// only looks at the K most-plausible replies. Together the two caps
	// keep depth-2 search well under 100 ms on a mid-game position.
	topCandidatesAtDepth0 = 60
	topCandidatesAtDepth1 = 20
)

// BestMove returns the move the engine prefers for `color`, or (zero, false)
// if there is no legal play (full board, game over, or color isn't on the
// turn). Callers should verify that `state.CurrentPlayer().Color == color`
// before invoking — the engine doesn't enforce turn order itself, it just
// picks the strongest candidate from `color`'s perspective.
func (e *Engine) BestMove(state *game.GameState, color game.Color) (game.Position, bool) {
	if state.IsOver() {
		return game.Position{}, false
	}

	candidates := rankedCandidates(state, color, topCandidatesAtDepth0)
	if len(candidates) == 0 {
		return game.Position{}, false
	}

	var best []game.Position
	bestScore := minScore
	alpha, beta := minScore, -minScore

	for _, c := range candidates {
		child := state.Clone()
		if _, err := child.ApplyMove(game.Move{Player: color, Pos: c.pos}, child.TurnStartedAt); err != nil {
			continue
		}
		// Standard negamax: recurse from opponent's perspective then
		// negate. The child's score is what the opponent thinks of the
		// position, so -child's-score is what we think of it.
		score := -negamax(child, opponentOf(child, color), searchDepth-1, -beta, -alpha)
		if score > bestScore {
			bestScore = score
			best = append(best[:0], c.pos)
		} else if score == bestScore {
			best = append(best, c.pos)
		}
		if score > alpha {
			alpha = score
		}
		// No β cutoff at the root — we want the full set of equally-best
		// moves so the random tiebreak has a non-trivial pool to pick from.
	}
	if len(best) == 0 {
		return game.Position{}, false
	}
	return best[e.rng.Intn(len(best))], true
}

// negamax returns the position's score from `mover`'s perspective with α-β
// pruning. Standard recursive form: a child's score (from opponent's POV)
// is negated to become this side's evaluation of the same node.
func negamax(state *game.GameState, mover game.Color, depth, alpha, beta int) int {
	if state.IsOver() {
		return terminalScore(state, mover)
	}
	if depth == 0 {
		return scorePosition(state, mover)
	}

	candidates := rankedCandidates(state, mover, topCandidatesAtDepth1)
	if len(candidates) == 0 {
		return scorePosition(state, mover)
	}

	best := minScore
	for _, c := range candidates {
		child := state.Clone()
		if _, err := child.ApplyMove(game.Move{Player: mover, Pos: c.pos}, child.TurnStartedAt); err != nil {
			continue
		}
		score := -negamax(child, opponentOf(child, mover), depth-1, -beta, -alpha)
		if score > best {
			best = score
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			break // β-cutoff: opponent has a better alternative earlier
		}
	}
	return best
}

// terminalScore is the eval at a game-over leaf. A win for the rooting
// player is hugely positive; a loss is hugely negative; a draw or multi-
// player no-winner finish is neutral.
func terminalScore(state *game.GameState, perspectiveColor game.Color) int {
	switch state.Winner {
	case perspectiveColor:
		return winScore
	case game.Empty:
		return 0
	default:
		return -winScore
	}
}

// scorePosition is the static evaluation function: how good is the current
// board for `perspectiveColor`? Combines (own offensive lines + captures)
// against (opponent's lines), and a small center pull as a tiebreaker.
func scorePosition(state *game.GameState, perspectiveColor game.Color) int {
	cfg := state.Config
	mineA5 := game.CountMaximalRuns(state.Board, perspectiveColor, 5)
	mineA4 := game.CountMaximalRuns(state.Board, perspectiveColor, 4)
	mineA3 := game.CountMaximalRuns(state.Board, perspectiveColor, 3)

	// Cumulative opponent threat across every other live player.
	oppA5, oppA4, oppA3 := 0, 0, 0
	for _, p := range state.Players {
		if p.Color == perspectiveColor {
			continue
		}
		oppA5 += game.CountMaximalRuns(state.Board, p.Color, 5)
		oppA4 += game.CountMaximalRuns(state.Board, p.Color, 4)
		oppA3 += game.CountMaximalRuns(state.Board, p.Color, 3)
	}

	mePlayer := playerByColor(state, perspectiveColor)
	myCaptures := 0
	if mePlayer != nil {
		myCaptures = mePlayer.CapturedPairs
	}
	oppCaptures := 0
	for _, p := range state.Players {
		if p.Color != perspectiveColor {
			oppCaptures += p.CapturedPairs
		}
	}

	// Threshold-aware: lines closer to the winning count weigh more, so the
	// bot prioritises completing its near-victory chains over scattering
	// new short runs.
	offense := mineA5*200 + mineA4*60 + mineA3*15 + myCaptures*40
	if cfg.CapturePairsWin > 0 {
		offense += myCaptures * (50 * myCaptures / max(cfg.CapturePairsWin, 1))
	}
	defense := oppA5*240 + oppA4*70 + oppA3*15 + oppCaptures*40

	return offense - defense
}

// rankedCandidates returns the top-K empty cells for `mover`, ranked by a
// quick 1-ply static evaluation. This serves two purposes:
//   - Move ordering: the most-promising branches are explored first so
//     alpha-beta prunes more aggressively.
//   - Truncation: we cap at `k` to bound the branching factor — full
//     enumeration on an 11-side hex is too many candidates for depth-2.
func rankedCandidates(state *game.GameState, mover game.Color, k int) []candidate {
	r := state.Board.Side - 1
	scored := make([]candidate, 0, 64)
	for q := -r; q <= r; q++ {
		for s := -r; s <= r; s++ {
			p := game.Position{Q: q, R: s}
			if !state.Board.In(p) || state.Board.At(p) != game.Empty {
				continue
			}
			scored = append(scored, candidate{pos: p, score: shallowScore(state, p, mover)})
		}
	}
	// Partial sort by score desc, stable enough for tiebreaking.
	sortDescending(scored)
	if len(scored) > k {
		scored = scored[:k]
	}
	return scored
}

type candidate struct {
	pos   game.Position
	score int
}

// shallowScore is the cheap 1-ply evaluator used purely to rank candidates
// for the search frontier. It mirrors the old `evaluate` heuristic so the
// pre-search ordering already favours captures, blocks, and extensions.
func shallowScore(state *game.GameState, p game.Position, color game.Color) int {
	sim := state.Board.Clone()
	sim.Set(p, color)
	captures := game.DetectCaptures(sim, p, color)
	for _, c := range captures {
		sim.Set(c.Pair[0], game.Empty)
		sim.Set(c.Pair[1], game.Empty)
	}
	if game.HasRun(sim, color, 6) {
		return winScore
	}

	cfg := state.Config
	mineA5 := game.CountMaximalRuns(sim, color, 5)
	mineA4 := game.CountMaximalRuns(sim, color, 4)
	if cfg.Align5ToWin > 0 && mineA5 >= cfg.Align5ToWin {
		return winScore - 1
	}
	if cfg.Align4ToWin > 0 && mineA4 >= cfg.Align4ToWin {
		return winScore - 2
	}

	oppColor := opponentOf(state, color)
	oppA5Before := game.CountMaximalRuns(state.Board, oppColor, 5)
	oppA4Before := game.CountMaximalRuns(state.Board, oppColor, 4)
	oppA5After := game.CountMaximalRuns(sim, oppColor, 5)
	oppA4After := game.CountMaximalRuns(sim, oppColor, 4)
	defense := (oppA5Before-oppA5After)*240 + (oppA4Before-oppA4After)*60

	// Extension-block bonus. CountMaximalRuns doesn't shift when we plug
	// the tail of an opponent's run (their existing length stays the
	// same; only their *potential* extension shrinks). The shallow
	// heuristic missed this entirely, which made tactical blocking moves
	// undervalued. Detect them directly: for each axis, count consecutive
	// opp stones immediately adjacent to p — that's the run we'd be
	// capping if we played here.
	for _, d := range game.Directions {
		opp := consecutiveSameColor(state.Board, p, d, oppColor) +
			consecutiveSameColor(state.Board, p, game.Position{Q: -d.Q, R: -d.R}, oppColor)
		if opp >= 3 {
			// Weight rises sharply at length 4–5 where the threat is
			// genuine (one more opp stone = a 5/6-run).
			defense += opp * opp * 8
		}
	}

	mineA5Before := game.CountMaximalRuns(state.Board, color, 5)
	mineA4Before := game.CountMaximalRuns(state.Board, color, 4)
	offense := (mineA5-mineA5Before)*120 + (mineA4-mineA4Before)*30
	captureScore := len(captures) * 80
	center := 10 - hexDistance(p, game.Position{})

	return defense + offense + captureScore + center
}

// consecutiveSameColor walks from `from` along `step` (exclusive of `from`)
// and returns the run length of cells matching `color`. Used by the
// extension-block heuristic to measure how big an opponent run we'd be
// capping by playing at `from`.
func consecutiveSameColor(b *game.Board, from, step game.Position, color game.Color) int {
	n := 0
	c := game.Position{Q: from.Q + step.Q, R: from.R + step.R}
	for b.In(c) && b.At(c) == color {
		n++
		c = game.Position{Q: c.Q + step.Q, R: c.R + step.R}
	}
	return n
}

// opponentOf returns the colour of whoever plays after `color`. In 2-player
// games this is the only other seat; in 3+ player games it's the next seat
// in rotation — the bot still treats anyone-not-me as a threat in
// scorePosition.
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sortDescending sorts scored candidates by score, highest first. We hand-
// roll a simple insertion-sort because slices are typically tiny after the
// initial gather (under 200 elements) and stable insertion gives us a
// deterministic order for equal scores.
func sortDescending(s []candidate) {
	for i := 1; i < len(s); i++ {
		c := s[i]
		j := i - 1
		for j >= 0 && s[j].score < c.score {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = c
	}
}
