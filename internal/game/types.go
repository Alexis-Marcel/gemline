package game

import "errors"

// Color identifies what is on a board intersection.
//   Empty    (0) — playable intersection, no stone
//   C1..C6   (1..6) — player colors
//   OffBoard (-1) — the storage slot is outside the hexagonal play area
//
// The underlying type is `int` rather than `uint8` so that []Color does not
// collide with Go's special-case JSON encoding of []byte (base64).
type Color int

const (
	OffBoard Color = -1
	Empty    Color = 0
	C1       Color = 1
	C2       Color = 2
	C3       Color = 3
	C4       Color = 4
	C5       Color = 5
	C6       Color = 6
)

const (
	MaxPlayers       = 6
	GemsPerPlayer    = 50
	DefaultBoardSide = 11 // intersections per side of the hexagon
)

func (c Color) String() string {
	switch c {
	case OffBoard:
		return " "
	case Empty:
		return "."
	case C1, C2, C3, C4, C5, C6:
		return string(rune('0' + c))
	default:
		return "?"
	}
}

// Position is an axial coordinate (q, r) on the hex grid. The origin (0, 0)
// is the center of the board. For a hex of side N, valid positions satisfy
//
//	|q| ≤ N-1   ∧   |r| ≤ N-1   ∧   |q+r| ≤ N-1
//
// which together describe the regular hexagonal outline.
type Position struct {
	Q, R int
}

// Player holds a player's running score. Alignment counters are computed
// on demand (see GameState.CountAlignments) rather than stored, so the
// scorecard cannot drift out of sync with the board.
//
// TimeRemainingMs is the chess-style clock — the amount of thinking time
// the player has left in total. The active player's clock ticks down
// between their TurnStartedAt and their next ApplyMove. A player whose
// clock reaches zero forfeits the game (WinTimeout).
type Player struct {
	Color           Color
	GemsRemaining   int
	CapturedPairs   int
	TimeRemainingMs int64
}

type Move struct {
	Player Color
	Pos    Position
}

type Capture struct {
	Capturer Color
	Victim   Color
	Pair     [2]Position
}

// MoveResult is everything that happened from a single placement: any
// captures triggered, and the win state if the move ended the game.
type MoveResult struct {
	Captures []Capture
	Winner   Color
	WinKind  WinKind
}

type WinKind int

const (
	WinNone WinKind = iota
	WinAlignment6
	WinAlignment5
	WinAlignment4
	WinCapture
	WinTimeout // the opponent ran out of time
)

func (k WinKind) String() string {
	switch k {
	case WinNone:
		return "none"
	case WinAlignment6:
		return "alignment6"
	case WinAlignment5:
		return "alignment5"
	case WinAlignment4:
		return "alignment4"
	case WinCapture:
		return "capture"
	case WinTimeout:
		return "timeout"
	default:
		return "?"
	}
}

var (
	ErrOutOfBounds  = errors.New("position out of bounds")
	ErrCellOccupied = errors.New("cell already occupied")
	ErrWrongTurn    = errors.New("not this player's turn")
	ErrGameOver     = errors.New("game is already over")
	ErrNoGemsLeft   = errors.New("player has no gems remaining")
	ErrFlagged      = errors.New("player has run out of time")
)
