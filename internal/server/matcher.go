package server

import (
	"context"
	"log/slog"
	"math"
	"time"
)

// matcherTickInterval is the gap between matchmaker passes. Short
// enough that users don't feel like they're staring at a spinner (the
// expected wait between clicking "find match" and the server reaching
// out is at most one interval), long enough that idle pods aren't
// hammering Postgres with empty FOR UPDATE SKIP LOCKED calls.
const matcherTickInterval = 1500 * time.Millisecond

// matcherPlayerCounts enumerates the room sizes the matcher supports.
// One pass per count per tick; each pass independently locks rows in
// matchmake_queue WHERE players = N.
var matcherPlayerCounts = []int{2, 3, 4, 5, 6}

// Enqueue inserts (or refreshes) the caller's matchmaking ticket. The
// underlying ON CONFLICT DO UPDATE makes re-clicking "find match"
// idempotent and bumps the user to the back of the queue rather than
// stacking duplicate rows.
func (s *Store) Enqueue(ctx context.Context, userID string, players int, mode string, rating int) error {
	return s.repo.EnqueueMatchmake(ctx, userID, players, mode, rating)
}

// CancelMatchmake removes the caller's ticket. Safe to call when no
// ticket exists (DELETE matches zero rows). Wired to both the explicit
// HTTP cancel endpoint and the lobby WS close handler so a user who
// navigates away stops occupying their slot.
func (s *Store) CancelMatchmake(ctx context.Context, userID string) error {
	return s.repo.CancelMatchmake(ctx, userID)
}

// StartMatcher launches the background goroutine that runs one
// matcher pass every matcherTickInterval. Returns immediately; the
// ticker stops when ctx is cancelled. Each match is reported to
// onMatched, which is responsible for fanning a 'match_found' event
// to each seated user via the lobby channel.
//
// Every pod calls StartMatcher independently: SKIP LOCKED on the
// queue rows means concurrent ticks pick disjoint batches without
// any coordination. There is no "the matcher" — there are N matchers,
// each happily doing their share.
func (s *Store) StartMatcher(ctx context.Context, log *slog.Logger, onMatched func([]MatchedSeat)) {
	if log == nil {
		log = slog.Default()
	}
	go func() {
		ticker := time.NewTicker(matcherTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.matcherTick(ctx, log, onMatched)
			}
		}
	}()
}

// matcherTick runs one round of matching across every supported player
// count. Errors on one count don't prevent the others from running —
// we want a transient hiccup on 1v1 to leave multi unaffected.
func (s *Store) matcherTick(ctx context.Context, log *slog.Logger, onMatched func([]MatchedSeat)) {
	for _, p := range matcherPlayerCounts {
		mode := RatingModeMulti
		if p == 2 {
			mode = RatingMode1v1
		}
		players := p
		seats, err := s.repo.MatchmakeTick(ctx, players, mode, func(qs []QueuedUser) [][]QueuedUser {
			if players == 2 {
				return pair1v1(qs, time.Now())
			}
			return pairMulti(qs, players)
		})
		if err != nil {
			log.Error("matcher tick", "players", players, "err", err)
			continue
		}
		if len(seats) > 0 && onMatched != nil {
			onMatched(seats)
		}
	}
}

// pair1v1 greedily pairs users by rating proximity, widening each
// user's tolerance band with their wait time. For every unmatched
// user in queue order, we find the remaining unmatched user with the
// smallest rating delta that falls inside the union of their bands
// (the more lenient of the two wins), pair them, and continue.
//
// Users with no acceptable partner stay in queue: next tick their
// band has grown by another matchBandGrowthPS points and someone
// they couldn't match this pass may be reachable now.
func pair1v1(qs []QueuedUser, now time.Time) [][]QueuedUser {
	n := len(qs)
	if n < 2 {
		return nil
	}
	matched := make([]bool, n)
	var out [][]QueuedUser
	for i := 0; i < n; i++ {
		if matched[i] {
			continue
		}
		bandI := scoreBandFor(now.Sub(qs[i].EnqueuedAt))
		bestJ := -1
		var bestDelta float64
		for j := i + 1; j < n; j++ {
			if matched[j] {
				continue
			}
			bandJ := scoreBandFor(now.Sub(qs[j].EnqueuedAt))
			band := bandI
			if bandJ > band {
				band = bandJ
			}
			delta := math.Abs(float64(qs[i].Rating - qs[j].Rating))
			if delta > band {
				continue
			}
			if bestJ == -1 || delta < bestDelta {
				bestJ = j
				bestDelta = delta
			}
		}
		if bestJ >= 0 {
			out = append(out, []QueuedUser{qs[i], qs[bestJ]})
			matched[i] = true
			matched[bestJ] = true
		}
	}
	return out
}

// pairMulti forms groups of exactly `players` users in FIFO order. We
// don't apply rating bands to 3+ player matches today — getting four
// people into a room at all is the binding constraint at this scale,
// not rating closeness. The oldest waiters land in the first group;
// later groups inherit the remaining order.
//
// Anyone left over (fewer than `players` unmatched at the tail) is
// rolled back into the queue for the next tick.
func pairMulti(qs []QueuedUser, players int) [][]QueuedUser {
	if len(qs) < players {
		return nil
	}
	var out [][]QueuedUser
	for i := 0; i+players <= len(qs); i += players {
		group := make([]QueuedUser, players)
		copy(group, qs[i:i+players])
		out = append(out, group)
	}
	return out
}
