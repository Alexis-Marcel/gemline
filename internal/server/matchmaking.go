package server

import (
	"math"
	"time"
)

// Matchmaking constants. The pairing tolerance grows with the candidate
// game's wait time so solo joiners at unusual ratings eventually pair with
// anyone — at the cost of a wider Elo gap the longer they wait.
const (
	matchBandBase      = 100.0                // ±100 the moment the room opens
	matchBandGrowthPS  = 10.0                 // grows by 10 pts/sec of room age
	matchBandMax       = 1000.0               // hard ceiling — at 90s wait, anyone goes
	matchPromoter1v1   = 0 * time.Second      // 1v1 always starts on AllSeated
	matchPromoter6P    = 0 * time.Second      // 6/6 multi → start immediately
	matchPromoter5P    = 5 * time.Second      // 5/6 → wait briefly for a 6th
	matchPromoter4P    = 10 * time.Second     // 4/6
	matchPromoter3P    = 20 * time.Second     // 3/6 — the floor before promotion
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

// multiPromotionThreshold returns the minimum wait time before a public
// multi-player game with `occupied` filled seats should auto-start. Six
// occupants → start immediately (the AllSeated path in Join already does
// that). Three is the floor — fewer than three and we won't start at all.
func multiPromotionThreshold(occupied int) time.Duration {
	switch {
	case occupied >= 6:
		return matchPromoter6P
	case occupied == 5:
		return matchPromoter5P
	case occupied == 4:
		return matchPromoter4P
	case occupied == 3:
		return matchPromoter3P
	default:
		// Sentinel: a value larger than any room will reasonably wait. The
		// caller treats "below threshold" as "don't promote yet"; this just
		// means "don't promote, ever, at this occupancy".
		return 24 * time.Hour
	}
}
