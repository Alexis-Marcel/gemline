package game

import "errors"

// Color identifies what is on an intersection: Empty (0), C1..C6 (players),
// OffBoard (-1, outside the hex). Backed by int, not uint8, so []Color isn't
// JSON-encoded as base64 like []byte.
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
	DefaultBoardSide = 11
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

// Position is an axial coordinate (q, r); origin is the board center. For a hex
// of side N, valid cells satisfy |q| ≤ N-1, |r| ≤ N-1, and |q+r| ≤ N-1.
type Position struct {
	Q, R int
}

// Player holds a player's running score. Alignment counts are computed on
// demand (CountAlignments), not stored, so they can't drift from the board.
// TimeRemainingMs is the chess clock; reaching zero forfeits (WinTimeout).
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

// MoveResult is the outcome of a single placement: captures and any win state.
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
	WinTimeout
	WinResign
	WinDraw // 2-player only
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
	case WinResign:
		return "resign"
	case WinDraw:
		return "draw"
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
