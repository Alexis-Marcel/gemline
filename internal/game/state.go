package game

import "time"

// Config holds the win thresholds, board side, and time control for a game.
// A 6-alignment is always an instant win, regardless of config.
type Config struct {
	BoardSide       int
	CapturePairsWin int // captured pairs required to win
	Align4ToWin     int // distinct maximal 4-alignments required
	Align5ToWin     int // distinct maximal 5-alignments required

	InitialTimeMs int64 // per-player chess-clock budget; 0 disables time control
	IncrementMs   int64 // Fischer bonus added after each move; 0 = none
}

const DefaultInitialTimeMs int64 = 10 * 60 * 1000 // 10 minutes

// DefaultConfig returns the rulebook thresholds for `numPlayers` (2..6).
// CapturePairsWin is in pairs; the rulebook lists gems (captured 2-by-2), so
// it is half the gem count shown in the inline comments.
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

// ConfigFor returns the rulebook thresholds for `numPlayers` while preserving
// the time-control fields from `base`. Used when the actual player count is
// known after creation; the create-time clock budget is carried through.
func ConfigFor(numPlayers int, base Config) Config {
	out := DefaultConfig(numPlayers)
	out.InitialTimeMs = base.InitialTimeMs
	out.IncrementMs = base.IncrementMs
	if base.BoardSide > 0 {
		out.BoardSide = base.BoardSide
	}
	return out
}

// GameState is an in-progress or finished game. Not safe for concurrent use;
// callers must serialize access externally.
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
		TurnStartedAt: time.Time{},
	}
}

// Clone returns a deep copy for speculative simulation (e.g. AI minimax).
// History is deliberately omitted: forward simulation doesn't need it, and
// skipping it keeps clones cheap.
func (g *GameState) Clone() *GameState {
	players := make([]Player, len(g.Players))
	copy(players, g.Players)
	return &GameState{
		Config:        g.Config,
		Board:         g.Board.Clone(),
		Players:       players,
		Turn:          g.Turn,
		Winner:        g.Winner,
		WinKind:       g.WinKind,
		TurnStartedAt: g.TurnStartedAt,
	}
}

// StartClock begins counting time against the first player. Call once after
// NewGame. No-op when time control is disabled.
func (g *GameState) StartClock(now time.Time) {
	if g.Config.InitialTimeMs > 0 {
		g.TurnStartedAt = now
	}
}

func (g *GameState) CurrentPlayer() *Player { return &g.Players[g.Turn] }

// IsOver reports whether the game is finished. A finished game has a Winner, or
// a non-zero WinKind without a winner (multi-player timeout — see Forfeit).
func (g *GameState) IsOver() bool { return g.Winner != Empty || g.WinKind != WinNone }

func (g *GameState) ClockEnabled() bool { return g.Config.InitialTimeMs > 0 }

// RemainingForActive returns the active player's clock at `now`, deducting time
// elapsed since TurnStartedAt. Returns the stored value when clocks are disabled.
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

// CountAlignments is a wrapper over CountMaximalRuns for callers (e.g. JSON
// DTOs) that don't import the rules helpers directly.
func (g *GameState) CountAlignments(color Color, length int) int {
	return CountMaximalRuns(g.Board, color, length)
}

// ApplyMove places a stone, resolves captures, updates win state, and advances
// the chess clock. The active player's elapsed time (now − TurnStartedAt) is
// deducted before the move; an exhausted clock fails with ErrFlagged.
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

// Forfeit ends the game by declaring `loser` out of time. With two players the
// survivor wins; with more, the game ends with no winner but WinKind ==
// WinTimeout. No-op if already over.
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
	g.Winner = Empty
	// Zero the loser's clock for clarity in the wire state.
	for i := range g.Players {
		if g.Players[i].Color == loser {
			g.Players[i].TimeRemainingMs = 0
		}
	}
}

// Resign ends the game by declaring `loser` voluntarily resigned. With two
// players the survivor wins; with more, no winner. Distinct from Forfeit
// because the win-kind affects post-game stats and rendering.
func (g *GameState) Resign(loser Color) {
	if g.IsOver() {
		return
	}
	g.WinKind = WinResign
	if len(g.Players) == 2 {
		for _, p := range g.Players {
			if p.Color != loser {
				g.Winner = p.Color
				return
			}
		}
	}
	g.Winner = Empty
}

// AgreeDraw ends a 2-player game in a draw. Callers must enforce the 2-player
// constraint: 3+ player draws aren't part of the published rules.
func (g *GameState) AgreeDraw() {
	if g.IsOver() {
		return
	}
	g.WinKind = WinDraw
	g.Winner = Empty
}

// checkWin returns the win kind achieved by `p`, or WinNone. Alignment wins are
// checked before captures so a 6-alignment takes precedence.
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
