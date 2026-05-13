package game

// Board holds the state of every intersection on a hexagonal grid of given
// side length. Storage uses a (2*Side-1) × (2*Side-1) row-major array. Slots
// outside the hexagonal outline are pre-filled with OffBoard so the wire
// format is self-describing for clients.
type Board struct {
	Side  int // intersections per side (e.g. 11 → 331 valid cells)
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

// idx converts an axial position to its row-major storage index. Callers
// must have checked In(p) first; passing an out-of-range position panics.
func (b *Board) idx(p Position) int {
	span := 2*b.Side - 1
	return (p.R+b.Side-1)*span + (p.Q + b.Side - 1)
}

// In reports whether p is a valid intersection on this hex board.
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

// Clone returns a deep copy. Useful for callers (e.g. the AI) that need to
// score speculative moves without mutating the live board.
func (b *Board) Clone() *Board {
	cells := make([]Color, len(b.Cells))
	copy(cells, b.Cells)
	return &Board{Side: b.Side, Cells: cells}
}

// Directions exposes the three line axes used for captures and alignments.
// The package-internal `directions` (lowercase) remains the source of truth;
// this is its read-only export for downstream code (e.g. the AI heuristic).
var Directions = directions

// directions are the three line axes along which captures and alignments are
// evaluated on a hex grid. Each entry is the axial unit vector for that line;
// negating one gives the opposite direction.
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
