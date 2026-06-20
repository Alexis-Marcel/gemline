// Package ai is a lightweight bot for computer players: a depth-2 α-β minimax
// over candidate moves with a heuristic leaf scorer. Two plies (my move +
// opponent's best reply) is enough to dodge obvious one-move tactical blunders
// without paying for deeper search. The goal is playable practice, not optimal play.
package ai

import (
	"math/rand"

	"github.com/alexis-marcel/gemline/internal/game"
)

// Engine is the public entry point. Its seeded PRNG makes move selection
// deterministic for tests.
type Engine struct {
	rng *rand.Rand
}

func NewEngine(seed int64) *Engine {
	return &Engine{rng: rand.New(rand.NewSource(seed))}
}

const (
	searchDepth = 2 // plies looked ahead: bot's move + opponent's best reply

	minScore = -1 << 30
	winScore = 1_000_000

	// Root candidate cap. Kept generous because shallowScore undervalues
	// defensive moves (blocking a 5-run's open end doesn't shift
	// CountMaximalRuns), so a small cap silently dropped critical blocks.
	// The two caps together keep depth-2 search well under 100 ms mid-game.
	topCandidatesAtDepth0 = 60
	topCandidatesAtDepth1 = 20
)

// BestMove returns the engine's preferred move for `color`, or (zero, false)
// if there is no legal play. It does not enforce turn order; callers should
// verify state.CurrentPlayer().Color == color.
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
		// Negamax: negate the opponent's view of the child to get ours.
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
		// No β cutoff at the root: keep all equally-best moves so the random
		// tiebreak has a pool to pick from.
	}
	if len(best) == 0 {
		return game.Position{}, false
	}
	return best[e.rng.Intn(len(best))], true
}

// negamax scores the position from `mover`'s perspective with α-β pruning.
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

// terminalScore evaluates a game-over leaf: win positive, loss negative,
// draw/no-winner neutral.
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

// scorePosition statically evaluates the board for `perspectiveColor`,
// weighing own offensive lines and captures against opponents'.
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

	// Longer lines weigh more so the bot completes near-victory chains rather
	// than scattering new short runs.
	offense := mineA5*200 + mineA4*60 + mineA3*15 + myCaptures*40
	if cfg.CapturePairsWin > 0 {
		offense += myCaptures * (50 * myCaptures / max(cfg.CapturePairsWin, 1))
	}
	defense := oppA5*240 + oppA4*70 + oppA3*15 + oppCaptures*40

	return offense - defense
}

// rankedCandidates returns the top-k empty cells for `mover` by shallowScore.
// Ranking improves α-β pruning; the cap bounds the branching factor (full
// enumeration on a side-11 hex is too wide for depth-2).
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

// shallowScore is the cheap 1-ply evaluator used to rank candidates,
// favouring captures, blocks, and extensions.
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

	// Extension-block bonus. CountMaximalRuns doesn't change when we plug the
	// tail of an opponent run (its length is unchanged, only its potential
	// extension shrinks), so blocking moves were undervalued. Detect them
	// directly: count opp stones adjacent to p along each axis.
	for _, d := range game.Directions {
		opp := consecutiveSameColor(state.Board, p, d, oppColor) +
			consecutiveSameColor(state.Board, p, game.Position{Q: -d.Q, R: -d.R}, oppColor)
		if opp >= 3 {
			// Weight rises sharply at 4–5, where one more stone is a 5/6-run.
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

// consecutiveSameColor returns the run length of `color` walking from `from`
// along `step`, excluding `from` itself.
func consecutiveSameColor(b *game.Board, from, step game.Position, color game.Color) int {
	n := 0
	c := game.Position{Q: from.Q + step.Q, R: from.R + step.R}
	for b.In(c) && b.At(c) == color {
		n++
		c = game.Position{Q: c.Q + step.Q, R: c.R + step.R}
	}
	return n
}

// opponentOf returns the color of the next seat in rotation after `color`.
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

// sortDescending sorts candidates by score, highest first. Hand-rolled
// insertion sort: slices are tiny and it gives a deterministic order for ties.
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
