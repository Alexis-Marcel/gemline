package server

import (
	"math"
	"time"
)

// 1v1 pairing tolerance grows with wait time so outlier ratings eventually
// pair with anyone, at the cost of a wider Elo gap. Multi-player wait
// thresholds live in matcher.go (multiWaitThreshold).
const (
	matchBandBase     = 100.0
	matchBandGrowthPS = 10.0
	matchBandMax      = 1000.0 // hard ceiling reached at ~90s wait
)

// scoreBandFor returns the maximum Elo delta a candidate game accepts given
// its wait time.
func scoreBandFor(age time.Duration) float64 {
	band := matchBandBase + age.Seconds()*matchBandGrowthPS
	if band > matchBandMax {
		return matchBandMax
	}
	return band
}

// withinBand reports whether two ratings are close enough for the given
// candidate age.
func withinBand(callerRating, candidateRating int, age time.Duration) bool {
	delta := math.Abs(float64(callerRating) - float64(candidateRating))
	return delta <= scoreBandFor(age)
}
