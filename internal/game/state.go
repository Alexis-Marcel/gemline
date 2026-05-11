package game

// Config holds the win thresholds and board side for a game.
type Config struct {
	BoardSide       int // intersections per side of the hexagonal board
	CapturePairsWin int // captured pairs required to win
	Align4ToWin     int // distinct maximal 4-alignments required
	Align5ToWin     int // distinct maximal 5-alignments required
	// A 6-alignment is always an instant win, regardless of config.
}

// DefaultConfig returns thresholds for `numPlayers` (2..6).
//
// The 2-player thresholds come from the published rules. Values for 3..6 are
// placeholders, biased toward keeping multi-player games tractable, and
// should be revisited once the official rulebook is available.
func DefaultConfig(numPlayers int) Config {
	cfg := Config{BoardSide: DefaultBoardSide}
	switch numPlayers {
	case 2:
		cfg.CapturePairsWin = 10
		cfg.Align4ToWin = 3
		cfg.Align5ToWin = 2
	case 3:
		cfg.CapturePairsWin = 8
		cfg.Align4ToWin = 3
		cfg.Align5ToWin = 2
	case 4:
		cfg.CapturePairsWin = 6
		cfg.Align4ToWin = 2
		cfg.Align5ToWin = 2
	case 5:
		cfg.CapturePairsWin = 5
		cfg.Align4ToWin = 2
		cfg.Align5ToWin = 1
	case 6:
		cfg.CapturePairsWin = 4
		cfg.Align4ToWin = 2
		cfg.Align5ToWin = 1
	default:
		return DefaultConfig(2)
	}
	return cfg
}

// GameState is an in-progress (or finished) game. It is not safe for
// concurrent use — callers must serialize access externally.
type GameState struct {
	Config  Config
	Board   *Board
	Players []Player
	Turn    int
	History []Move
	Winner  Color
	WinKind WinKind
}

func NewGame(playerColors []Color, cfg Config) *GameState {
	players := make([]Player, len(playerColors))
	for i, c := range playerColors {
		players[i] = Player{Color: c, GemsRemaining: GemsPerPlayer}
	}
	return &GameState{
		Config:  cfg,
		Board:   NewBoard(cfg.BoardSide),
		Players: players,
	}
}

func (g *GameState) CurrentPlayer() *Player { return &g.Players[g.Turn] }

func (g *GameState) IsOver() bool { return g.Winner != Empty }

// CountAlignments returns the number of maximal runs of `color` of length
// exactly `length`. Convenience wrapper for callers that don't want to
// import the rules helpers directly (e.g. JSON DTOs).
func (g *GameState) CountAlignments(color Color, length int) int {
	return CountMaximalRuns(g.Board, color, length)
}

// ApplyMove places a stone, resolves captures, and updates win state. The
// returned MoveResult lists the captures triggered and, if the game ended,
// the winner and the kind of win.
func (g *GameState) ApplyMove(m Move) (MoveResult, error) {
	var res MoveResult
	if g.IsOver() {
		return res, ErrGameOver
	}
	if g.CurrentPlayer().Color != m.Player {
		return res, ErrWrongTurn
	}
	if !g.Board.In(m.Pos) {
		return res, ErrOutOfBounds
	}
	if g.Board.At(m.Pos) != Empty {
		return res, ErrCellOccupied
	}
	cur := g.CurrentPlayer()
	if cur.GemsRemaining <= 0 {
		return res, ErrNoGemsLeft
	}

	g.Board.Set(m.Pos, m.Player)
	cur.GemsRemaining--
	g.History = append(g.History, m)

	captures := DetectCaptures(g.Board, m.Pos, m.Player)
	for _, c := range captures {
		g.Board.Set(c.Pair[0], Empty)
		g.Board.Set(c.Pair[1], Empty)
	}
	cur.CapturedPairs += len(captures)
	res.Captures = captures

	if kind := g.checkWin(cur); kind != WinNone {
		g.Winner = cur.Color
		g.WinKind = kind
		res.Winner = cur.Color
		res.WinKind = kind
		return res, nil
	}

	g.Turn = (g.Turn + 1) % len(g.Players)
	return res, nil
}

// checkWin returns the kind of win achieved by `p`, or WinNone. Alignment
// wins are checked before capture wins so that a 6-alignment takes
// precedence over any concurrent capture threshold.
func (g *GameState) checkWin(p *Player) WinKind {
	if HasRun(g.Board, p.Color, 6) {
		return WinAlignment6
	}
	if g.Config.Align5ToWin > 0 && CountMaximalRuns(g.Board, p.Color, 5) >= g.Config.Align5ToWin {
		return WinAlignment5
	}
	if g.Config.Align4ToWin > 0 && CountMaximalRuns(g.Board, p.Color, 4) >= g.Config.Align4ToWin {
		return WinAlignment4
	}
	if g.Config.CapturePairsWin > 0 && p.CapturedPairs >= g.Config.CapturePairsWin {
		return WinCapture
	}
	return WinNone
}
