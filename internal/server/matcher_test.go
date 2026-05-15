package server

import (
	"testing"
	"time"
)

// pair1v1 + pairMulti are pure: same input → same pairing, no DB, no
// time-of-day surprises. Tests here pin the contract the matcher ticker
// relies on. The integration story (FOR UPDATE SKIP LOCKED, NOTIFY,
// game creation in tx) is tested at the repo level with a real DB.

func TestPair1v1_EmptyOrSingle(t *testing.T) {
	if got := pair1v1(nil, time.Now()); len(got) != 0 {
		t.Fatalf("nil queue: want no pairs, got %+v", got)
	}
	one := []QueuedUser{{UserID: "a", Rating: 1200, EnqueuedAt: time.Now()}}
	if got := pair1v1(one, time.Now()); len(got) != 0 {
		t.Fatalf("single user: want no pairs, got %+v", got)
	}
}

func TestPair1v1_CloseRatingsPairImmediately(t *testing.T) {
	now := time.Now()
	// Delta 30 < band 100 (age=0) — easy match on the first tick.
	qs := []QueuedUser{
		{UserID: "a", Rating: 1200, EnqueuedAt: now},
		{UserID: "b", Rating: 1230, EnqueuedAt: now},
	}
	got := pair1v1(qs, now)
	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("want one pair of 2, got %+v", got)
	}
	if got[0][0].UserID != "a" || got[0][1].UserID != "b" {
		t.Fatalf("want a paired with b, got %s + %s", got[0][0].UserID, got[0][1].UserID)
	}
}

func TestPair1v1_FarRatingsDontPairAtZeroAge(t *testing.T) {
	now := time.Now()
	// Delta 300 > band 100 (age=0) — they stay in queue.
	qs := []QueuedUser{
		{UserID: "a", Rating: 1000, EnqueuedAt: now},
		{UserID: "b", Rating: 1300, EnqueuedAt: now},
	}
	if got := pair1v1(qs, now); len(got) != 0 {
		t.Fatalf("ratings too far apart at age 0: want no pair, got %+v", got)
	}
}

func TestPair1v1_AgeWidensBandEnoughToPair(t *testing.T) {
	now := time.Now()
	// User a has been waiting 30s → band = 100 + 30*10 = 400 (delta
	// 300 fits). Either user's wider band wins, so this pair forms
	// even though b just enqueued.
	qs := []QueuedUser{
		{UserID: "a", Rating: 1000, EnqueuedAt: now.Add(-30 * time.Second)},
		{UserID: "b", Rating: 1300, EnqueuedAt: now},
	}
	got := pair1v1(qs, now)
	if len(got) != 1 {
		t.Fatalf("age widening should pair: got %+v", got)
	}
}

func TestPair1v1_GreedyPicksClosestRatingPartner(t *testing.T) {
	now := time.Now()
	// a is the oldest, scans b and c. b's delta = 100, c's delta = 20.
	// Greedy picks c. b is left for the next tick.
	qs := []QueuedUser{
		{UserID: "a", Rating: 1200, EnqueuedAt: now.Add(-5 * time.Second)},
		{UserID: "b", Rating: 1100, EnqueuedAt: now.Add(-3 * time.Second)},
		{UserID: "c", Rating: 1220, EnqueuedAt: now.Add(-1 * time.Second)},
	}
	got := pair1v1(qs, now)
	if len(got) != 1 {
		t.Fatalf("want exactly one pair (third stays in queue), got %d pairs: %+v", len(got), got)
	}
	pair := got[0]
	if pair[0].UserID != "a" || pair[1].UserID != "c" {
		t.Fatalf("want a + c (smallest rating delta), got %s + %s", pair[0].UserID, pair[1].UserID)
	}
}

func TestPair1v1_MultiplePairsFromBatch(t *testing.T) {
	now := time.Now()
	qs := []QueuedUser{
		{UserID: "a", Rating: 1200, EnqueuedAt: now},
		{UserID: "b", Rating: 1210, EnqueuedAt: now},
		{UserID: "c", Rating: 1500, EnqueuedAt: now},
		{UserID: "d", Rating: 1490, EnqueuedAt: now},
	}
	got := pair1v1(qs, now)
	if len(got) != 2 {
		t.Fatalf("want 2 pairs (close-ratings cluster around 1200 and 1500), got %d: %+v", len(got), got)
	}
}

func TestPair1v1_OnceMatchedNotReusedInSameTick(t *testing.T) {
	now := time.Now()
	// b is closer to a than c is, AND closer to c than a is. The
	// greedy pass takes (a,b) first; c is then left alone (no other
	// unmatched candidate in band).
	qs := []QueuedUser{
		{UserID: "a", Rating: 1200, EnqueuedAt: now},
		{UserID: "b", Rating: 1210, EnqueuedAt: now},
		{UserID: "c", Rating: 1215, EnqueuedAt: now},
	}
	got := pair1v1(qs, now)
	if len(got) != 1 {
		t.Fatalf("want 1 pair, got %d: %+v", len(got), got)
	}
	matched := map[string]bool{}
	for _, p := range got {
		for _, u := range p {
			if matched[u.UserID] {
				t.Fatalf("user %s appears in two pairs in the same tick", u.UserID)
			}
			matched[u.UserID] = true
		}
	}
}

// pairMulti now decides:
//   - below quorum (<3) → wait
//   - full or over (≥players) → immediate group of `players`
//   - in between → wait until oldest queuer has aged past threshold(N)
//
// The tests below pin each branch.

func now0() time.Time { return time.Unix(0, 0).UTC() }

func queued(now time.Time, ids ...string) []QueuedUser {
	out := make([]QueuedUser, len(ids))
	for i, id := range ids {
		out[i] = QueuedUser{UserID: id, EnqueuedAt: now}
	}
	return out
}

func TestPairMulti_BelowQuorumWaits(t *testing.T) {
	qs := queued(now0(), "a", "b")
	if got := pairMulti(qs, 6, now0().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("2 users with quorum=3: no group should form even after long wait, got %+v", got)
	}
}

func TestPairMulti_FullQueueStartsImmediately(t *testing.T) {
	qs := queued(now0(), "a", "b", "c", "d", "e", "f")
	got := pairMulti(qs, 6, now0())
	if len(got) != 1 || len(got[0]) != 6 {
		t.Fatalf("6 users / players=6: want immediate group of 6, got %+v", got)
	}
}

func TestPairMulti_OverflowCapsAtPlayers(t *testing.T) {
	// pairMulti never returns more than `players` per group. With 8 in
	// queue and players=6, the first 6 leave together; the remaining 2
	// stay queued (below quorum next tick).
	qs := queued(now0(), "a", "b", "c", "d", "e", "f", "g", "h")
	got := pairMulti(qs, 6, now0())
	if len(got) != 1 || len(got[0]) != 6 {
		t.Fatalf("8 users / players=6: want one group of 6, got %+v", got)
	}
}

func TestPairMulti_ThreePlayersWaitUntilThreshold(t *testing.T) {
	enq := now0()
	qs := queued(enq, "a", "b", "c")
	// Below threshold: 3 users at quorum need 20s wait.
	if got := pairMulti(qs, 6, enq.Add(19*time.Second)); len(got) != 0 {
		t.Fatalf("3 users at 19s: still below 20s threshold, got %+v", got)
	}
	// At threshold: group forms with exactly 3.
	got := pairMulti(qs, 6, enq.Add(20*time.Second))
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("3 users at 20s: want group of 3, got %+v", got)
	}
}

func TestPairMulti_FivePlayersWaitFiveSeconds(t *testing.T) {
	enq := now0()
	qs := queued(enq, "a", "b", "c", "d", "e")
	if got := pairMulti(qs, 6, enq.Add(4*time.Second)); len(got) != 0 {
		t.Fatalf("5 users at 4s: still below 5s threshold, got %+v", got)
	}
	got := pairMulti(qs, 6, enq.Add(5*time.Second))
	if len(got) != 1 || len(got[0]) != 5 {
		t.Fatalf("5 users at 5s: want group of 5, got %+v", got)
	}
}

func TestPairMulti_FIFOOrderPreserved(t *testing.T) {
	// SQL feeds rows ORDER BY enqueued_at ASC; pairMulti must keep that
	// order so seat indices are stable.
	enq := now0()
	qs := queued(enq, "a", "b", "c")
	got := pairMulti(qs, 6, enq.Add(30*time.Second))
	if got[0][0].UserID != "a" || got[0][1].UserID != "b" || got[0][2].UserID != "c" {
		t.Fatalf("want FIFO order a,b,c, got %+v", got[0])
	}
}

func TestPairMulti_AgeMeasuredFromOldest(t *testing.T) {
	// "a" enqueued at T=0, "b" at T=15s, "c" at T=18s. At T=20s, oldest
	// (a) has waited 20s. With 3 users the threshold is 20s → start.
	a := now0()
	qs := []QueuedUser{
		{UserID: "a", EnqueuedAt: a},
		{UserID: "b", EnqueuedAt: a.Add(15 * time.Second)},
		{UserID: "c", EnqueuedAt: a.Add(18 * time.Second)},
	}
	got := pairMulti(qs, 6, a.Add(20*time.Second))
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("3 users with oldest aged 20s: want group of 3, got %+v", got)
	}
}
