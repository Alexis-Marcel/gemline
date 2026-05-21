package server

import (
	"math"
	"time"
)

// Matchmaking constants. The pairing tolerance grows with the candidate
// game's wait time so solo joiners at unusual ratings eventually pair with
// anyone — at the cost of a wider Elo gap the longer they wait.
//
// Multi-player wait thresholds live in matcher.go (multiWaitThreshold)
// alongside the pairing function that consumes them.
const (
	matchBandBase     = 100.0  // ±100 the moment the room opens
	matchBandGrowthPS = 10.0   // grows by 10 pts/sec of room age
	matchBandMax      = 1000.0 // hard ceiling — at 90s wait, anyone goes
)

// scoreBandFor returns the maximum Elo delta a candidate game is willing to
// accept given how long it has been waiting. Older games are more permissive
// — they've earned the right to widen their search.
func scoreBandFor(age time.Duration) float64 {
	band := matchBandBase + age.Seconds()*matchBandGrowthPS
	if band > matchBandMax {
		return matchBandMax
	}
	return band
}

// withinBand reports whether two ratings are close enough for the given
// candidate age. Used by 1v1 matchmaking to pair similarly-rated players
// while still guaranteeing eventual matchmaking for outlier ratings.
func withinBand(callerRating, candidateRating int, age time.Duration) bool {
	delta := math.Abs(float64(callerRating) - float64(candidateRating))
	return delta <= scoreBandFor(age)
}
