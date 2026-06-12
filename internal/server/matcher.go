package server

import (
	"context"
	"log/slog"
	"math"
	"time"
)

const matcherTickInterval = 1500 * time.Millisecond

// matcherPlayerCounts enumerates the room sizes the matcher supports.
var matcherPlayerCounts = []int{2, 3, 4, 5, 6}

// Enqueue inserts (or refreshes) the caller's matchmaking ticket. ON CONFLICT
// DO UPDATE makes re-clicking idempotent and bumps the user to the back of the
// queue rather than stacking duplicate rows.
func (s *Store) Enqueue(ctx context.Context, userID string, players int, mode string, rating int) error {
	return s.repo.EnqueueMatchmake(ctx, userID, players, mode, rating)
}

// CancelMatchmake removes the caller's ticket. Safe to call when no ticket
// exists (DELETE matches zero rows).
func (s *Store) CancelMatchmake(ctx context.Context, userID string) error {
	return s.repo.CancelMatchmake(ctx, userID)
}

// CurrentMatchmadeGame returns the id of the game the user was matched into, or
// "" — the durable signal the search page polls when the match_found push is
// missed.
func (s *Store) CurrentMatchmadeGame(ctx context.Context, userID string) (string, error) {
	return s.repo.CurrentMatchmadeGame(ctx, userID)
}

// QueueUpdate is published to each user still in queue after a tick. Count is
// the bucket's current size; ETASeconds is the remaining wait before a multi
// room of that size auto-starts (nil for 1v1 or under-quorum buckets).
type QueueUpdate struct {
	UserID     string
	Players    int
	Mode       string
	Count      int
	ETASeconds *int
}

// StartMatcher runs one matcher pass every matcherTickInterval until ctx is
// cancelled. Every pod calls this independently: SKIP LOCKED on the queue rows
// means concurrent ticks pick disjoint batches with no coordination.
func (s *Store) StartMatcher(ctx context.Context, log *slog.Logger, onMatched func([]MatchedSeat), onQueueUpdate func([]QueueUpdate)) {
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
				s.matcherTick(ctx, log, onMatched, onQueueUpdate)
			}
		}
	}()
}

// matcherTick runs one round across every player count. An error on one count
// doesn't prevent the others from running.
func (s *Store) matcherTick(ctx context.Context, log *slog.Logger, onMatched func([]MatchedSeat), onQueueUpdate func([]QueueUpdate)) {
	for _, p := range matcherPlayerCounts {
		mode := RatingModeMulti
		if p == 2 {
			mode = RatingMode1v1
		}
		players := p
		seats, err := s.repo.MatchmakeTick(ctx, players, mode, func(qs []QueuedUser) [][]QueuedUser {
			now := time.Now()
			if players == 2 {
				return pair1v1(qs, now)
			}
			return pairMulti(qs, players, now)
		})
		if err != nil {
			log.Error("matcher tick", "players", players, "err", err)
			continue
		}
		if len(seats) > 0 && onMatched != nil {
			onMatched(seats)
		}
		if onQueueUpdate != nil {
			snap, snapErr := s.repo.MatchmakeQueueSnapshot(ctx, players, mode)
			if snapErr != nil {
				log.Warn("matcher snapshot", "players", players, "err", snapErr)
				continue
			}
			if updates := buildQueueUpdates(snap, players, mode, time.Now()); len(updates) > 0 {
				onQueueUpdate(updates)
			}
		}
	}
}

// buildQueueUpdates turns a bucket snapshot into one QueueUpdate per user. ETA
// is the remaining wait before a multi room of the current size auto-starts
// (clamped to 0); nil for 1v1 and for under-quorum multi buckets.
func buildQueueUpdates(snap []QueuedUser, players int, mode string, now time.Time) []QueueUpdate {
	count := len(snap)
	if count == 0 {
		return nil
	}
	var eta *int
	if mode == RatingModeMulti && count >= minMultiSeats {
		threshold := multiWaitThreshold(count)
		remaining := threshold - now.Sub(snap[0].EnqueuedAt)
		if remaining < 0 {
			remaining = 0
		}
		secs := int(remaining.Seconds())
		eta = &secs
	}
	out := make([]QueueUpdate, 0, count)
	for _, u := range snap {
		out = append(out, QueueUpdate{
			UserID:     u.UserID,
			Players:    players,
			Mode:       mode,
			Count:      count,
			ETASeconds: eta,
		})
	}
	return out
}

// pair1v1 greedily pairs users by rating proximity, using the more lenient of
// the two wait-widened bands. Users with no acceptable partner stay in queue;
// next tick their band has grown and a previously-unreachable peer may match.
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

// pairMulti groups multi-player queuers into a single room of dynamic size
// (3..players); no rating bands apply. Triggers:
//   - >= players queued → form a group of exactly players, start now.
//   - >= minMultiSeats and oldest aged past multiWaitThreshold(len(qs)) →
//     form a group of len(qs).
//   - otherwise → wait.
//
// At most one group per tick: 8 queued with players=6 sends the first 6 in and
// leaves 2 behind (they need a third before they match).
func pairMulti(qs []QueuedUser, players int, now time.Time) [][]QueuedUser {
	n := len(qs)
	if n < minMultiSeats {
		return nil
	}
	if n >= players {
		group := make([]QueuedUser, players)
		copy(group, qs[:players])
		return [][]QueuedUser{group}
	}
	oldestAge := now.Sub(qs[0].EnqueuedAt)
	if oldestAge < multiWaitThreshold(n) {
		return nil
	}
	group := make([]QueuedUser, n)
	copy(group, qs)
	return [][]QueuedUser{group}
}

// minMultiSeats is the quorum below which a multi room never starts.
const minMultiSeats = 3

func multiWaitThreshold(occupied int) time.Duration {
	switch {
	case occupied >= 6:
		return 0
	case occupied == 5:
		return 5 * time.Second
	case occupied == 4:
		return 10 * time.Second
	case occupied == 3:
		return 20 * time.Second
	default:
		// Sentinel for "never start at this size" so callers can compare
		// age < threshold without special-casing below-quorum.
		return 24 * time.Hour
	}
}
