package game

import "testing"

func setStones(b *Board, stones map[Position]Color) {
	for p, c := range stones {
		b.Set(p, c)
	}
}

// pos is a tiny constructor that keeps test setups readable: pos(q, r).
func pos(q, r int) Position { return Position{Q: q, R: r} }

// ---- Board geometry ----

func TestBoard_HexBounds(t *testing.T) {
	b := NewBoard(11)
	// Center is in.
	if !b.In(pos(0, 0)) {
		t.Fatal("center should be in")
	}
	// Six corner intersections of the hex of side 11 are at distance 10.
	corners := []Position{
		pos(10, 0), pos(-10, 0),
		pos(0, 10), pos(0, -10),
		pos(10, -10), pos(-10, 10),
	}
	for _, c := range corners {
		if !b.In(c) {
			t.Fatalf("corner %v should be in", c)
		}
	}
	// Just outside any corner is out.
	outs := []Position{
		pos(11, 0), pos(0, 11), pos(11, -10), pos(-11, 10),
		pos(6, 6), // q+r = 12, exceeds 10
	}
	for _, o := range outs {
		if b.In(o) {
			t.Fatalf("position %v should be out of bounds", o)
		}
	}
}

func TestBoard_OffBoardSentinel(t *testing.T) {
	b := NewBoard(11)
	// A storage slot outside the hex should read as OffBoard.
	// (6, 6) has q+r = 12 > 10, so it's off-board but inside the (2N-1)² array.
	if b.Cells[b.idx(pos(6, 6))] != OffBoard {
		t.Fatalf("off-board slot should be OffBoard, got %v", b.Cells[b.idx(pos(6, 6))])
	}
	// A valid empty cell should read as Empty.
	if b.At(pos(0, 0)) != Empty {
		t.Fatalf("center should be Empty, got %v", b.At(pos(0, 0)))
	}
}

// ---- DetectCaptures ----

func TestCapture_RightFlanker_EastWest(t *testing.T) {
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-2, 0): C1,
		pos(-1, 0): C2,
		pos(0, 0):  C2,
	})
	b.Set(pos(1, 0), C1)
	caps := DetectCaptures(b, pos(1, 0), C1)
	if len(caps) != 1 || caps[0].Victim != C2 {
		t.Fatalf("want 1 capture of C2, got %+v", caps)
	}
}

func TestCapture_LeftFlanker_EastWest(t *testing.T) {
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-1, 0): C2,
		pos(0, 0):  C2,
		pos(1, 0):  C1,
	})
	b.Set(pos(-2, 0), C1)
	if got := DetectCaptures(b, pos(-2, 0), C1); len(got) != 1 {
		t.Fatalf("left flanker should also capture, got %d", len(got))
	}
}

func TestCapture_NW_SE(t *testing.T) {
	// Direction (0, 1): r varies, q constant.
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(2, -2): C1,
		pos(2, -1): C3,
		pos(2, 0):  C3,
	})
	b.Set(pos(2, 1), C1)
	if got := DetectCaptures(b, pos(2, 1), C1); len(got) != 1 {
		t.Fatalf("NW-SE capture: want 1, got %d", len(got))
	}
}

func TestCapture_NE_SW(t *testing.T) {
	// Direction (1, -1): q increases, r decreases.
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-2, 2): C1,
		pos(-1, 1): C4,
		pos(0, 0):  C4,
	})
	b.Set(pos(1, -1), C1)
	if got := DetectCaptures(b, pos(1, -1), C1); len(got) != 1 {
		t.Fatalf("NE-SW capture: want 1, got %d", len(got))
	}
}

func TestCapture_RequiresSameVictimColor(t *testing.T) {
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-2, 0): C1, pos(-1, 0): C2, pos(0, 0): C3,
	})
	b.Set(pos(1, 0), C1)
	if got := DetectCaptures(b, pos(1, 0), C1); len(got) != 0 {
		t.Fatalf("mixed victims should not capture, got %d", len(got))
	}
}

func TestCapture_NoCaptureOnTriplet(t *testing.T) {
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-3, 0): C1, pos(-2, 0): C2, pos(-1, 0): C2, pos(0, 0): C2,
	})
	b.Set(pos(1, 0), C1)
	if got := DetectCaptures(b, pos(1, 0), C1); len(got) != 0 {
		t.Fatalf("triplet should not be captured, got %d", len(got))
	}
}

func TestCapture_MultipleAxes(t *testing.T) {
	b := NewBoard(11)
	// At placement (1, 0) we trigger captures on two axes simultaneously.
	// East-West (right flanker): (-2,0)=C1, (-1,0)=C2, (0,0)=C2, (1,0)=placed
	// NE-SW (left flanker via direction (1,-1)):
	//   (1, 0)=placed, (2,-1)=C3, (3,-2)=C3, (4,-3)=C1
	setStones(b, map[Position]Color{
		pos(-2, 0): C1, pos(-1, 0): C2, pos(0, 0): C2,
		pos(2, -1): C3, pos(3, -2): C3, pos(4, -3): C1,
	})
	b.Set(pos(1, 0), C1)
	if got := DetectCaptures(b, pos(1, 0), C1); len(got) != 2 {
		t.Fatalf("want 2 captures across axes, got %d", len(got))
	}
}

func TestCapture_NoSelfCaptureOnSuicide(t *testing.T) {
	// Placing C1 between two C2 flankers ([C2][C1][C1][C2]) must NOT trigger
	// a capture of the placer.
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-2, 0): C2,
		pos(-1, 0): C1, // pre-existing C1 between the C2 flankers
		pos(1, 0):  C2,
	})
	b.Set(pos(0, 0), C1)
	caps := DetectCaptures(b, pos(0, 0), C1)
	if len(caps) != 0 {
		t.Fatalf("suicide should not self-capture, got %d", len(caps))
	}
	if b.At(pos(-1, 0)) != C1 || b.At(pos(0, 0)) != C1 {
		t.Fatalf("placer stones should remain")
	}
}

// ---- Runs ----

func TestCountMaximalRuns_ExactLengthOnly(t *testing.T) {
	b := NewBoard(11)
	// Two separate 4-runs and one 5-run on different rows.
	for q := -5; q <= -2; q++ {
		b.Set(pos(q, 0), C1)
	}
	for q := -5; q <= -2; q++ {
		b.Set(pos(q, 2), C1)
	}
	for q := 1; q <= 5; q++ {
		b.Set(pos(q, -4), C1)
	}
	if got := CountMaximalRuns(b, C1, 4); got != 2 {
		t.Fatalf("want 2 maximal 4-runs, got %d", got)
	}
	if got := CountMaximalRuns(b, C1, 5); got != 1 {
		t.Fatalf("want 1 maximal 5-run, got %d", got)
	}
}

func TestHasRun(t *testing.T) {
	b := NewBoard(11)
	for q := -3; q <= 2; q++ {
		b.Set(pos(q, 0), C1)
	}
	if !HasRun(b, C1, 6) {
		t.Fatalf("expected HasRun to find a 6-run")
	}
	if HasRun(b, C1, 7) {
		t.Fatalf("did not expect a 7-run")
	}
}

// ---- ApplyMove plumbing ----

func TestApplyMove_AlternatesAndDecrementsGems(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if g.CurrentPlayer().Color != C2 {
		t.Fatalf("turn did not advance to C2")
	}
	if g.Players[0].GemsRemaining != GemsPerPlayer-1 {
		t.Fatalf("gem count not decremented")
	}
}

func TestApplyMove_RejectsWrongTurn(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	if _, err := g.ApplyMove(Move{Player: C2, Pos: pos(0, 0)}); err != ErrWrongTurn {
		t.Fatalf("want ErrWrongTurn, got %v", err)
	}
}

func TestApplyMove_RejectsOccupied(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	_, _ = g.ApplyMove(Move{Player: C1, Pos: pos(0, 0)})
	if _, err := g.ApplyMove(Move{Player: C2, Pos: pos(0, 0)}); err != ErrCellOccupied {
		t.Fatalf("want ErrCellOccupied, got %v", err)
	}
}

func TestApplyMove_RejectsOutOfBounds(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	// q+r exceeds side-1 → off-board.
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(6, 6)}); err != ErrOutOfBounds {
		t.Fatalf("want ErrOutOfBounds for (6,6), got %v", err)
	}
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(11, 0)}); err != ErrOutOfBounds {
		t.Fatalf("want ErrOutOfBounds for (11,0), got %v", err)
	}
}

func TestApplyMove_RejectsAfterGameOver(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	for q := -3; q <= 1; q++ {
		g.Board.Set(pos(q, 0), C1)
	}
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(2, 0)}); err != nil {
		t.Fatal(err)
	}
	if !g.IsOver() {
		t.Fatalf("game should be over after the winning placement")
	}
	if _, err := g.ApplyMove(Move{Player: C2, Pos: pos(0, 1)}); err != ErrGameOver {
		t.Fatalf("want ErrGameOver, got %v", err)
	}
}

func TestApplyMove_CaptureRemovesGemsAndIncrementsPairs(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.Board.Set(pos(-2, 0), C1)
	g.Board.Set(pos(-1, 0), C2)
	g.Board.Set(pos(0, 0), C2)
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Captures) != 1 {
		t.Fatalf("want 1 capture, got %d", len(res.Captures))
	}
	if g.Board.At(pos(-1, 0)) != Empty || g.Board.At(pos(0, 0)) != Empty {
		t.Fatalf("captured cells should be Empty")
	}
	if g.Players[0].CapturedPairs != 1 {
		t.Fatalf("captured pairs counter should be 1, got %d", g.Players[0].CapturedPairs)
	}
}

// ---- Win conditions ----

func TestWin_Alignment6(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	for q := -3; q <= 1; q++ {
		g.Board.Set(pos(q, 0), C1)
	}
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(2, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner != C1 || res.WinKind != WinAlignment6 {
		t.Fatalf("want C1 win by alignment6, got winner=%v kind=%v", res.Winner, res.WinKind)
	}
}

func TestWin_Alignment5_BelowThresholdDoesNotWin(t *testing.T) {
	cfg := DefaultConfig(2) // Align5ToWin = 2
	g := NewGame([]Color{C1, C2}, cfg)
	for q := -3; q <= 0; q++ {
		g.Board.Set(pos(q, 0), C1)
	}
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner != Empty {
		t.Fatalf("one 5-alignment should not win (threshold=2), got winner=%v", res.Winner)
	}
}

func TestWin_Alignment5_AtThreshold(t *testing.T) {
	cfg := Config{BoardSide: 11, Align5ToWin: 1}
	g := NewGame([]Color{C1, C2}, cfg)
	for q := -3; q <= 0; q++ {
		g.Board.Set(pos(q, 0), C1)
	}
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner != C1 || res.WinKind != WinAlignment5 {
		t.Fatalf("want C1 win by alignment5, got winner=%v kind=%v", res.Winner, res.WinKind)
	}
}

func TestWin_Alignment4_AtThreshold(t *testing.T) {
	cfg := Config{BoardSide: 11, Align4ToWin: 1}
	g := NewGame([]Color{C1, C2}, cfg)
	for q := -2; q <= 0; q++ {
		g.Board.Set(pos(q, 0), C1)
	}
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner != C1 || res.WinKind != WinAlignment4 {
		t.Fatalf("want C1 win by alignment4, got winner=%v kind=%v", res.Winner, res.WinKind)
	}
}

func TestWin_Capture(t *testing.T) {
	cfg := Config{BoardSide: 11, CapturePairsWin: 1}
	g := NewGame([]Color{C1, C2}, cfg)
	g.Board.Set(pos(-2, 0), C1)
	g.Board.Set(pos(-1, 0), C2)
	g.Board.Set(pos(0, 0), C2)
	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner != C1 || res.WinKind != WinCapture {
		t.Fatalf("want C1 win by capture, got winner=%v kind=%v", res.Winner, res.WinKind)
	}
}

// ---- Stringers (smoke tests) ----

func TestColor_String(t *testing.T) {
	cases := map[Color]string{
		OffBoard: " ",
		Empty:    ".",
		C1:       "1",
		C6:       "6",
		Color(42): "?",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Color(%d).String() = %q, want %q", c, got, want)
		}
	}
}

func TestWinKind_String(t *testing.T) {
	cases := map[WinKind]string{
		WinNone:       "none",
		WinAlignment6: "alignment6",
		WinAlignment5: "alignment5",
		WinAlignment4: "alignment4",
		WinCapture:    "capture",
		WinKind(99):   "?",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("WinKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// ---- Config ----

func TestDefaultConfig_AllPlayerCounts(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 6} {
		cfg := DefaultConfig(n)
		if cfg.BoardSide != DefaultBoardSide {
			t.Errorf("n=%d: BoardSide=%d, want %d", n, cfg.BoardSide, DefaultBoardSide)
		}
		if cfg.CapturePairsWin <= 0 || cfg.Align4ToWin <= 0 || cfg.Align5ToWin <= 0 {
			t.Errorf("n=%d: non-positive threshold in %+v", n, cfg)
		}
	}
	// Out-of-range falls back to the 2-player config.
	if got := DefaultConfig(99); got != DefaultConfig(2) {
		t.Errorf("DefaultConfig(99) = %+v, want fallback to 2-player config", got)
	}
}

// ---- Multi-player rotation ----

func TestApplyMove_ThreePlayerRotation(t *testing.T) {
	g := NewGame([]Color{C1, C2, C3}, DefaultConfig(3))
	plays := []struct {
		who Color
		p   Position
	}{
		{C1, pos(-3, 0)},
		{C2, pos(-2, 1)},
		{C3, pos(-1, 2)},
		{C1, pos(0, -3)}, // wraps back to player 0
	}
	for i, m := range plays {
		if _, err := g.ApplyMove(Move{Player: m.who, Pos: m.p}); err != nil {
			t.Fatalf("step %d (%v): %v", i, m.who, err)
		}
	}
	if g.CurrentPlayer().Color != C2 {
		t.Fatalf("after 4 moves, want C2 to play, got %v", g.CurrentPlayer().Color)
	}
}

// ---- Exhausted stock ----

func TestApplyMove_RejectsWhenNoGemsLeft(t *testing.T) {
	g := NewGame([]Color{C1, C2}, DefaultConfig(2))
	g.Players[0].GemsRemaining = 0
	if _, err := g.ApplyMove(Move{Player: C1, Pos: pos(0, 0)}); err != ErrNoGemsLeft {
		t.Fatalf("want ErrNoGemsLeft, got %v", err)
	}
}

// ---- Capture chaining on the same axis ----

func TestCapture_TwoCapturesOnSameAxis(t *testing.T) {
	// Setup on row r=0:
	//   q = -3 -2 -1  0   1   2  3
	//       C1 C2 C2 (J) C2  C2 C1
	// Placing C1 at q=0 is the right flanker of the left sandwich and the
	// left flanker of the right sandwich → both captures fire on axis (1,0).
	b := NewBoard(11)
	setStones(b, map[Position]Color{
		pos(-3, 0): C1,
		pos(-2, 0): C2, pos(-1, 0): C2,
		pos(1, 0): C2, pos(2, 0): C2,
		pos(3, 0): C1,
	})
	b.Set(pos(0, 0), C1)
	caps := DetectCaptures(b, pos(0, 0), C1)
	if len(caps) != 2 {
		t.Fatalf("want 2 captures on the same axis, got %d: %+v", len(caps), caps)
	}
}

// ---- Interaction of capture and alignments ----

// TestCaptureBreaksOpponentAlignment verifies that the (computed-on-demand)
// alignment counts reflect the board after captures: a victim stone that was
// part of a 4-run on a crossing axis no longer is once it is captured.
func TestCaptureBreaksOpponentAlignment(t *testing.T) {
	g := NewGame([]Color{C1, C2}, Config{BoardSide: 11})
	// East-West capture setup along r=0: [C1][C2][C2][placed C1].
	g.Board.Set(pos(-2, 0), C1)
	g.Board.Set(pos(-1, 0), C2)
	g.Board.Set(pos(0, 0), C2)
	// C2 also has a vertical 4-run (axis (0,1)) passing through (-1, 0):
	//   (-1,-2) (-1,-1) (-1,0) (-1,1)
	g.Board.Set(pos(-1, -2), C2)
	g.Board.Set(pos(-1, -1), C2)
	g.Board.Set(pos(-1, 1), C2)

	if got := g.CountAlignments(C2, 4); got != 1 {
		t.Fatalf("setup: want 1 four-run for C2, got %d", got)
	}

	res, err := g.ApplyMove(Move{Player: C1, Pos: pos(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Captures) != 1 {
		t.Fatalf("want 1 capture, got %d", len(res.Captures))
	}
	if got := g.CountAlignments(C2, 4); got != 0 {
		t.Fatalf("after capture, want 0 four-runs for C2, got %d", got)
	}
}
