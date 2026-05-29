package game

// Board stores a hex grid in a (2*Side-1)² row-major array. Slots outside the
// hexagonal outline hold OffBoard so the wire format is self-describing.
type Board struct {
	Side  int
	Cells []Color
}

func NewBoard(side int) *Board {
	span := 2*side - 1
	b := &Board{Side: side, Cells: make([]Color, span*span)}
	for i := -(side - 1); i <= side-1; i++ {
		for j := -(side - 1); j <= side-1; j++ {
			p := Position{Q: i, R: j}
			if !b.In(p) {
				b.Cells[b.idx(p)] = OffBoard
			}
		}
	}
	return b
}

// idx maps an axial position to its row-major index; caller must ensure In(p).
func (b *Board) idx(p Position) int {
	span := 2*b.Side - 1
	return (p.R+b.Side-1)*span + (p.Q + b.Side - 1)
}

func (b *Board) In(p Position) bool {
	r := b.Side - 1
	return abs(p.Q) <= r && abs(p.R) <= r && abs(p.Q+p.R) <= r
}

func (b *Board) At(p Position) Color {
	return b.Cells[b.idx(p)]
}

func (b *Board) Set(p Position, c Color) {
	b.Cells[b.idx(p)] = c
}

// Clone returns a deep copy, used to score speculative moves off the live board.
func (b *Board) Clone() *Board {
	cells := make([]Color, len(b.Cells))
	copy(cells, b.Cells)
	return &Board{Side: b.Side, Cells: cells}
}

// Directions is the read-only export of directions for downstream code.
var Directions = directions

// directions are the three hex line axes for captures and alignments. Each is
// an axial unit vector; negating it gives the opposite direction.
var directions = [3]Position{
	{1, 0},  // east ↔ west
	{0, 1},  // south-east ↔ north-west
	{1, -1}, // north-east ↔ south-west
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
