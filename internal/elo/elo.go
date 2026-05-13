// Package elo implements the classic Elo rating computation used to update
// player ratings after a finished 2-player rated game. The math is pure and
// has no external dependencies, so it can be tested with deterministic
// inputs and reused from any caller.
package elo

import "math"

// K is the development coefficient applied to every rating update. A higher K
// means ratings swing more per game. 32 is the chess.com value for unrated
// and provisional players; FIDE uses K ∈ [10, 40] depending on rating bands.
// v1 keeps a single K across the board — banded K is a follow-up.
const K = 32

// DefaultRating is the starting Elo for any player who has not yet played a
// rated game. Matches chess.com's default.
const DefaultRating = 1200

// Outcome is the result of a game from one player's perspective.
type Outcome int

const (
	Loss Outcome = iota
	Draw
	Win
)

func (o Outcome) score() float64 {
	switch o {
	case Win:
		return 1.0
	case Draw:
		return 0.5
	default:
		return 0.0
	}
}

// Expected returns the expected score (in [0, 1]) of player A against player
// B given their current ratings. 1.0 = certain win; 0.5 = even match.
func Expected(ratingA, ratingB int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(ratingB-ratingA)/400.0))
}

// Update returns the post-game rating of a player who entered the game at
// `rating`, played against an opponent rated `opponent`, and ended with
// `outcome` (Win/Loss/Draw). The new rating is rounded to the nearest
// integer — sub-integer Elo doesn't really mean anything to humans.
func Update(rating, opponent int, outcome Outcome) int {
	exp := Expected(rating, opponent)
	delta := float64(K) * (outcome.score() - exp)
	return rating + int(math.Round(delta))
}
