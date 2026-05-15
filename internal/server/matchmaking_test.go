package server

import (
	"testing"
	"time"
)

func TestScoreBand_GrowsWithAge(t *testing.T) {
	a := scoreBandFor(0)
	b := scoreBandFor(5 * time.Second)
	c := scoreBandFor(30 * time.Second)
	if !(a < b && b < c) {
		t.Fatalf("band must widen with age, got %v < %v < %v", a, b, c)
	}
}

func TestScoreBand_CapsAtMax(t *testing.T) {
	// Anything past ~90s should hit the ceiling.
	if got := scoreBandFor(10 * time.Minute); got != matchBandMax {
		t.Fatalf("expected cap at %v, got %v", matchBandMax, got)
	}
}

func TestWithinBand_TightAtZeroAge(t *testing.T) {
	if !withinBand(1500, 1450, 0) {
		t.Fatalf("|1500-1450|=50 should be within ±%v band at age 0", matchBandBase)
	}
	if withinBand(1500, 1300, 0) {
		t.Fatalf("|1500-1300|=200 should be outside ±%v band at age 0", matchBandBase)
	}
}

func TestWithinBand_WidensOverTime(t *testing.T) {
	// 200 pts apart is rejected at t=0, accepted at t=20s (band grows to
	// 100 + 200 = 300).
	if withinBand(1500, 1300, 0) {
		t.Fatalf("200-pt gap should not pair instantly")
	}
	if !withinBand(1500, 1300, 20*time.Second) {
		t.Fatalf("200-pt gap should pair after 20s wait")
	}
}

func TestMultiWaitThreshold_Schedule(t *testing.T) {
	// 6+ players → start immediately; thresholds widen as the queue
	// shrinks. The exact numbers are part of the matchmaking UX so
	// they're pinned here.
	cases := []struct {
		occupied int
		want     time.Duration
	}{
		{6, 0},
		{5, 5 * time.Second},
		{4, 10 * time.Second},
		{3, 20 * time.Second},
	}
	for _, c := range cases {
		if got := multiWaitThreshold(c.occupied); got != c.want {
			t.Errorf("occupied=%d: want %v, got %v", c.occupied, c.want, got)
		}
	}
}

func TestMultiWaitThreshold_BelowFloorIsSentinel(t *testing.T) {
	// Fewer than minMultiSeats → return a very large duration so any
	// `age < threshold` check trivially refuses to start. Callers should
	// short-circuit on the count before consulting the threshold, but
	// the sentinel is here as a safety net.
	if got := multiWaitThreshold(2); got < time.Hour {
		t.Fatalf("≤2 occupants should never trigger a start, got %v", got)
	}
}
