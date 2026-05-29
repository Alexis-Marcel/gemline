package elo

import "testing"

func TestExpected_EqualRatingsIsHalf(t *testing.T) {
	if got := Expected(1500, 1500); got != 0.5 {
		t.Fatalf("equal ratings should give expected=0.5, got %v", got)
	}
}

func TestExpected_HigherIsMoreThanHalf(t *testing.T) {
	if got := Expected(1600, 1400); !(got > 0.5 && got < 1.0) {
		t.Fatalf("higher-rated player should expect >0.5 and <1.0, got %v", got)
	}
}

func TestUpdate_EqualRatingsWin(t *testing.T) {
	// At equal ratings the winner gains exactly K/2 = 16, the loser drops 16.
	winner := Update(1500, 1500, Win)
	loser := Update(1500, 1500, Loss)
	if winner != 1516 || loser != 1484 {
		t.Fatalf("equal-ratings win/loss should swing ±16, got winner=%d loser=%d", winner, loser)
	}
}

func TestUpdate_EqualRatingsDraw(t *testing.T) {
	if got := Update(1500, 1500, Draw); got != 1500 {
		t.Fatalf("equal-ratings draw should be a no-op, got %d", got)
	}
}

func TestUpdate_UpsetGainsMore(t *testing.T) {
	// An underdog (low expected score) gains a lot from beating a favourite,
	// but never more than K.
	gain := Update(1100, 1700, Win) - 1100
	if gain <= K/2 {
		t.Fatalf("upset win should gain more than K/2=%d points, got +%d", K/2, gain)
	}
	if gain > K {
		t.Fatalf("a single Elo update cannot exceed K=%d, got +%d", K, gain)
	}
}

func TestUpdate_FavoriteLossIsExpensive(t *testing.T) {
	// The favourite's loss must equal the underdog's gain (zero-sum).
	winnerGain := Update(1100, 1700, Win) - 1100
	loserLoss := 1700 - Update(1700, 1100, Loss)
	if winnerGain != loserLoss {
		t.Fatalf("Elo should be zero-sum: winner gained %d but loser lost %d", winnerGain, loserLoss)
	}
}

func TestUpdateMulti_ZeroSum(t *testing.T) {
	// The winner's gain must exactly equal the table's total loss.
	cases := []struct {
		winner int
		opps   []int
	}{
		{1500, []int{1500, 1500, 1500}},       // even table
		{1800, []int{1200, 1400, 1600}},       // strong winner
		{1100, []int{1700, 1700, 1700}},       // huge upset
		{1500, []int{1500, 1500}},             // 3-player game
		{1400, []int{1300, 1500, 1700, 1200}}, // 5 players
	}
	for _, c := range cases {
		oppIDs := make([]string, len(c.opps))
		for i := range oppIDs {
			oppIDs[i] = "opp"
		}
		results := UpdateMulti("winner", c.winner, oppIDs, c.opps)
		// Sum of deltas across everyone must be 0.
		total := 0
		for i, r := range results {
			var prev int
			if i == 0 {
				prev = c.winner
			} else {
				prev = c.opps[i-1]
			}
			total += r.NewRating - prev
		}
		if total != 0 {
			t.Errorf("winner=%d opps=%v: total delta=%d, want 0", c.winner, c.opps, total)
		}
	}
}

func TestUpdateMulti_WinnerGainsAgainstStrongerField(t *testing.T) {
	results := UpdateMulti("w", 1100, []string{"a", "b", "c"}, []int{1700, 1700, 1700})
	winnerGain := results[0].NewRating - 1100
	if winnerGain <= K/2 {
		t.Fatalf("upset win should gain more than K/2=%d, got +%d", K/2, winnerGain)
	}
}

func TestUpdate_ZeroSum(t *testing.T) {
	// Points swapped between two players must net to zero.
	cases := []struct{ a, b int }{
		{1200, 1200},
		{1400, 1300},
		{1000, 1800},
		{1234, 1567},
	}
	for _, c := range cases {
		aAfter := Update(c.a, c.b, Win)
		bAfter := Update(c.b, c.a, Loss)
		if (aAfter - c.a) != (c.b - bAfter) {
			t.Fatalf("not zero-sum for a=%d b=%d: a gained %d, b lost %d",
				c.a, c.b, aAfter-c.a, c.b-bAfter)
		}
	}
}
