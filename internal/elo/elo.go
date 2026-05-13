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

// MultiResult is a single player's updated rating after a multi-player
// rated game. Returned by UpdateMulti — index 0 is the winner.
type MultiResult struct {
	UserID    string
	NewRating int
	Result    rune // 'W' for winner, 'L' for the rest
}

// UpdateMulti applies the "average opponent" extension of Elo to a
// finished N-player game (N >= 3). The winner gains points by virtue of
// having beaten the average rating of the rest of the field; the losers
// split that same total loss equally so the system stays zero-sum across
// the table. With N=2 this degenerates to a regular pairwise Update, but
// callers should prefer Update for 2-player to keep behaviour identical
// to the pre-multi era.
//
// winnerID/winnerRating identify the player who triggered the win
// condition. opponentIDs/opponentRatings are the rest of the seated
// players (must be the same length, same ordering).
func UpdateMulti(
	winnerID string, winnerRating int,
	opponentIDs []string, opponentRatings []int,
) []MultiResult {
	n := len(opponentIDs)
	if n == 0 || n != len(opponentRatings) {
		return nil
	}

	avg := 0
	for _, r := range opponentRatings {
		avg += r
	}
	avg /= n

	newWinner := Update(winnerRating, avg, Win)
	gain := newWinner - winnerRating
	// Split the loss as evenly as possible across the losers. Rounding to
	// int means we may be off by ±1 from a perfect zero-sum total; we
	// pin the difference onto the first loser so the books balance.
	perLoss := gain / n
	remainder := gain - perLoss*n // can be negative if gain is negative

	out := make([]MultiResult, 0, n+1)
	out = append(out, MultiResult{UserID: winnerID, NewRating: newWinner, Result: 'W'})
	for i, id := range opponentIDs {
		loss := perLoss
		if i == 0 {
			loss += remainder
		}
		out = append(out, MultiResult{
			UserID:    id,
			NewRating: opponentRatings[i] - loss,
			Result:    'L',
		})
	}
	return out
}
