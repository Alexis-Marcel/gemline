// Package elo implements the classic Elo rating computation for updating player
// ratings after a finished rated game. The math is pure, with no dependencies.
package elo

import "math"

// K is the development coefficient: higher K swings ratings more per game. 32
// matches chess.com for provisional players. A single K is used across all
// bands for now.
const K = 32

// DefaultRating is the starting Elo for a player with no rated games.
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

// Expected returns A's expected score in [0,1] against B (1.0 = certain win,
// 0.5 = even match).
func Expected(ratingA, ratingB int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(ratingB-ratingA)/400.0))
}

// Update returns the post-game rating for a player who entered at `rating`
// against `opponent` with the given `outcome`, rounded to the nearest integer.
func Update(rating, opponent int, outcome Outcome) int {
	exp := Expected(rating, opponent)
	delta := float64(K) * (outcome.score() - exp)
	return rating + int(math.Round(delta))
}

// MultiResult is one player's updated rating from UpdateMulti; index 0 is the winner.
type MultiResult struct {
	UserID    string
	NewRating int
	Result    rune // 'W' for winner, 'L' for the rest
}

// UpdateMulti applies the "average opponent" Elo extension to a finished
// N-player game: the winner beats the field's average rating, and the losers
// split that gain equally to keep the table zero-sum. Prefer Update for
// 2-player. opponentIDs and opponentRatings must match in length and order.
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
	// Split the loss across losers; pin the integer-rounding remainder onto
	// the first loser so the table stays exactly zero-sum.
	perLoss := gain / n
	remainder := gain - perLoss*n // negative if gain is negative

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
