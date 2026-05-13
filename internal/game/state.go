package game

import "time"

// Config holds the win thresholds, board side, and time control for a game.
type Config struct {
	BoardSide       int // intersections per side of the hexagonal board
	CapturePairsWin int // captured pairs required to win
	Align4ToWin     int // distinct maximal 4-alignments required
	Align5ToWin     int // distinct maximal 5-alignments required
	// A 6-alignment is always an instant win, regardless of config.

	// InitialTimeMs is the chess-clock budget per player at the start of the
	// game. Zero disables time control entirely (clocks aren't tracked).
	InitialTimeMs int64
	// IncrementMs is the Fischer-style bonus added to a player's clock after
	// every move they make. Zero = no increment.
	IncrementMs int64
}

const DefaultInitialTimeMs int64 = 10 * 60 * 1000 // 10 minutes

// DefaultConfig returns thresholds for `numPlayers` (2..6), straight from the
// printed rulebook (page with the "Nombre de joueurs" table). A 6-alignment
// is always an instant win; the other columns are the count of *distinct*
// maximal runs the player must hold simultaneously. CapturePairsWin is the
// number of captured *pairs* — the rulebook lists gems (gobbled 2-by-2), so
// the value here is half the gem count.
func DefaultConfig(numPlayers int) Config {
	cfg := Config{
		BoardSide:     DefaultBoardSide,
		InitialTimeMs: DefaultInitialTimeMs,
		IncrementMs:   0,
	}
	switch numPlayers {
	case 2:
		cfg.CapturePairsWin = 12 // 24 gems
		cfg.Align4ToWin = 8
		cfg.Align5ToWin = 3
	case 3:
		cfg.CapturePairsWin = 10 // 20 gems
		cfg.Align4ToWin = 6
		cfg.Align5ToWin = 3
	case 4:
		cfg.CapturePairsWin = 9 // 18 gems
		cfg.Align4ToWin = 5
		cfg.Align5ToWin = 2
	case 5:
		cfg.CapturePairsWin = 7 // 14 gems
		cfg.Align4ToWin = 4
		cfg.Align5ToWin = 2
	case 6:
		cfg.CapturePairsWin = 6 // 12 gems
		cfg.Align4ToWin = 4
		cfg.Align5ToWin = 2
	default:
		return DefaultConfig(2)
	}
	return cfg
}

// GameState is an in-progress (or finished) game. It is not safe for
// concurrent use — callers must serialize access externally.
type GameState struct {
	Config        Config
	Board         *Board
	Players       []Player
	Turn          int
	History       []Move
	Winner        Color
	WinKind       WinKind
	TurnStartedAt time.Time // when the current player's clock started ticking
}

func NewGame(playerColors []Color, cfg Config) *GameState {
	players := make([]Player, len(playerColors))
	for i, c := range playerColors {
		players[i] = Player{
			Color:           c,
			GemsRemaining:   GemsPerPlayer,
			TimeRemainingMs: cfg.InitialTimeMs,
		}
	}
	return &GameState{
		Config:        cfg,
		Board:         NewBoard(cfg.BoardSide),
		Players:       players,
		TurnStartedAt: time.Time{}, // zero value; set on first move via clock-disabled fallback
	}
}

// StartClock initialises the turn-start timestamp. Call once after NewGame
// to begin counting time against the first player. If time control is
// disabled (Config.InitialTimeMs == 0), this is a no-op.
func (g *GameState) StartClock(now time.Time) {
	if g.Config.InitialTimeMs > 0 {
		g.TurnStartedAt = now
	}
}

func (g *GameState) CurrentPlayer() *Player { return &g.Players[g.Turn] }

// IsOver reports whether the game is finished. A finished game either has
// a Winner (most cases) or a non-zero WinKind without a winner (multi-player
// timeout — see Forfeit).
func (g *GameState) IsOver() bool { return g.Winner != Empty || g.WinKind != WinNone }

// ClockEnabled reports whether time control is active for this game.
func (g *GameState) ClockEnabled() bool { return g.Config.InitialTimeMs > 0 }

// RemainingForActive returns the active player's clock value if the game
// were to be checked right now, accounting for the elapsed time since
// TurnStartedAt. When clocks are disabled it returns the player's stored
// TimeRemainingMs unchanged.
func (g *GameState) RemainingForActive(now time.Time) int64 {
	if !g.ClockEnabled() || g.IsOver() {
		return g.CurrentPlayer().TimeRemainingMs
	}
	if g.TurnStartedAt.IsZero() {
		return g.CurrentPlayer().TimeRemainingMs
	}
	elapsed := now.Sub(g.TurnStartedAt).Milliseconds()
	r := g.CurrentPlayer().TimeRemainingMs - elapsed
	if r < 0 {
		return 0
	}
	return r
}

// CountAlignments returns the number of maximal runs of `color` of length
// exactly `length`. Convenience wrapper for callers that don't want to
// import the rules helpers directly (e.g. JSON DTOs).
func (g *GameState) CountAlignments(color Color, length int) int {
	return CountMaximalRuns(g.Board, color, length)
}

// ApplyMove places a stone, resolves captures, updates win state, and
// advances the chess clock. `now` is the move's timestamp; the active
// player's elapsed time (now − TurnStartedAt) is deducted from their clock
// before the move is applied. If they have already used up their time the
// move fails with ErrFlagged.
func (g *GameState) ApplyMove(m Move, now time.Time) (MoveResult, error) {
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

	if g.ClockEnabled() && !g.TurnStartedAt.IsZero() {
		elapsed := now.Sub(g.TurnStartedAt).Milliseconds()
		if elapsed < 0 {
			elapsed = 0 // tolerate small clock skews
		}
		if elapsed > cur.TimeRemainingMs {
			return res, ErrFlagged
		}
		cur.TimeRemainingMs -= elapsed
		cur.TimeRemainingMs += g.Config.IncrementMs
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
	if g.ClockEnabled() {
		g.TurnStartedAt = now
	}
	return res, nil
}

// Forfeit ends the game by declaring `loser` out of time. With exactly two
// players, the surviving player is recorded as the winner; with more, the
// game ends with no winner (Winner == Empty) but WinKind == WinTimeout.
// No-op if the game is already over.
func (g *GameState) Forfeit(loser Color) {
	if g.IsOver() {
		return
	}
	g.WinKind = WinTimeout
	if len(g.Players) == 2 {
		for _, p := range g.Players {
			if p.Color != loser {
				g.Winner = p.Color
				return
			}
		}
	}
	// 3+ players: end the game without a winner.
	g.Winner = Empty
	// Mark the loser's clock at zero for clarity in the wire state.
	for i := range g.Players {
		if g.Players[i].Color == loser {
			g.Players[i].TimeRemainingMs = 0
		}
	}
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
