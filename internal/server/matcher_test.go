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

func TestPairMulti_NotEnoughUsers(t *testing.T) {
	qs := []QueuedUser{{UserID: "a"}, {UserID: "b"}}
	if got := pairMulti(qs, 4); len(got) != 0 {
		t.Fatalf("2 users for a 4-player game: want no group, got %+v", got)
	}
}

func TestPairMulti_ExactlyOneGroup(t *testing.T) {
	qs := []QueuedUser{{UserID: "a"}, {UserID: "b"}, {UserID: "c"}, {UserID: "d"}}
	got := pairMulti(qs, 4)
	if len(got) != 1 || len(got[0]) != 4 {
		t.Fatalf("want exactly one group of 4, got %+v", got)
	}
}

func TestPairMulti_MultipleGroups(t *testing.T) {
	qs := []QueuedUser{
		{UserID: "a"}, {UserID: "b"}, {UserID: "c"},
		{UserID: "d"}, {UserID: "e"}, {UserID: "f"},
	}
	got := pairMulti(qs, 3)
	if len(got) != 2 || len(got[0]) != 3 || len(got[1]) != 3 {
		t.Fatalf("6 users / players=3: want 2 groups of 3, got %+v", got)
	}
}

func TestPairMulti_LeftoversStayInQueue(t *testing.T) {
	qs := []QueuedUser{
		{UserID: "a"}, {UserID: "b"}, {UserID: "c"}, {UserID: "d"},
	}
	got := pairMulti(qs, 3)
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("4 users / players=3: want 1 group of 3 (d left over), got %+v", got)
	}
	for _, u := range got[0] {
		if u.UserID == "d" {
			t.Fatalf("d should not be in the first group, got %+v", got)
		}
	}
}

func TestPairMulti_FIFOOrder(t *testing.T) {
	// Caller passes rows sorted by enqueued_at ASC (the SQL does it).
	// pairMulti should preserve that order in the resulting groups.
	qs := []QueuedUser{
		{UserID: "a"}, {UserID: "b"}, {UserID: "c"},
	}
	got := pairMulti(qs, 3)
	if got[0][0].UserID != "a" || got[0][1].UserID != "b" || got[0][2].UserID != "c" {
		t.Fatalf("want FIFO order a,b,c, got %+v", got[0])
	}
}
