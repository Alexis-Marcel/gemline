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

// QueueUpdate is published by the matcher to each user still in queue
// after a tick. Count is the bucket's current size; ETASeconds is the
// remaining wait before a multi room of that size auto-starts (nil for
// 1v1 or when the bucket is below quorum and has no deterministic ETA).
type QueueUpdate struct {
	UserID     string
	Players    int
	Mode       string
	Count      int
	ETASeconds *int
}

// StartMatcher launches the background goroutine that runs one
// matcher pass every matcherTickInterval. Returns immediately; the
// ticker stops when ctx is cancelled. Each match is reported to
// onMatched (fans match_found over lobby WS); each post-tick queue
// snapshot is reported to onQueueUpdate (fans queue_update so waiting
// users see a live count + ETA on the matchmaking screen).
//
// Every pod calls StartMatcher independently: SKIP LOCKED on the
// queue rows means concurrent ticks pick disjoint batches without
// any coordination. There is no "the matcher" — there are N matchers,
// each happily doing their share.
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

// matcherTick runs one round of matching across every supported player
// count. Errors on one count don't prevent the others from running —
// we want a transient hiccup on 1v1 to leave multi unaffected. After
// each tick we re-read the bucket (non-locking) to fan a queue_update
// event to anyone still waiting; pairing and notifying are decoupled
// so a slow snapshot read can't delay the match itself.
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

// buildQueueUpdates turns a bucket snapshot into one QueueUpdate per
// user. For multi, ETA is the remaining wait before the room of the
// current size auto-starts (clamped to 0); for 1v1 ETA stays nil
// (pairing depends on rating proximity, not a wall-clock countdown).
// Anyone in an under-quorum multi bucket gets a nil ETA too — there's
// nothing meaningful to display until at least minMultiSeats arrive.
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

// pairMulti groups multi-player queuers into a single room of dynamic
// size (3..players). Rating bands are not applied to 3+ player matches —
// rating closeness matters less when the binding constraint is "get a
// group of humans onto the board at all".
//
// Trigger conditions:
//   - If `players` or more are queued → form a group of exactly `players`
//     (max-out the room, start immediately).
//   - Else if at least minMultiSeats queuers are present AND the oldest
//     has waited past multiWaitThreshold(len(qs)) → form a group of
//     len(qs). The threshold shrinks as the queue grows so a near-full
//     queue starts faster than a barely-quorate one (see thresholds).
//   - Otherwise → wait.
//
// At most one group per tick: we never split the queue into multiple
// concurrent rooms. If 8 users queue simultaneously, the first 6 go in
// and the remaining 2 stay (they'll need a third before they match).
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

// Multi-player matchmaking thresholds. The minimum quorum (3) is the
// floor below which we never start; the per-size waits taper from 0s at
// six (start instantly when full) up to 20s at three (give the queue a
// chance to grow before committing to a small room).
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
		// Sentinel for "never start at this size" — used as a guard so
		// callers can compare age < threshold without special-casing
		// "below quorum".
		return 24 * time.Hour
	}
}
