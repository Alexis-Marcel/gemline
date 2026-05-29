package server

import (
	"time"

	"github.com/alexis/gemline/internal/game"
)

type createGameRequest struct {
	Players    int    `json:"players"`
	Visibility string `json:"visibility,omitempty"` // "public" | "private" (default)
	// Name is consumed only for anonymous callers — authenticated users
	// have their display name pulled from the profile server-side. Empty
	// for anonymous = 400.
	Name string `json:"name,omitempty"`
}

// matchmakeRequest is the body of POST /api/games/matchmake. The frontend uses
// players=2 (1v1) or 4 (multi), but the API accepts any 2..6.
type matchmakeRequest struct {
	Players int `json:"players"`
}

type joinGameRequest struct {
	Name string `json:"name"`
	Seat *int   `json:"seat,omitempty"` // omit for "any free seat"
}

type moveRequest struct {
	Q int `json:"q"`
	R int `json:"r"`
}

type seatDTO struct {
	Index int        `json:"index"`
	Color game.Color `json:"color"`
	Name  string     `json:"name"`
	// UserID is the public Supabase id for authenticated seats (empty for
	// anon/bots), surfaced so the frontend can link a seat to its profile page.
	UserID   string `json:"userId,omitempty"`
	Occupied bool   `json:"occupied"`
	IsBot    bool   `json:"isBot"`
}

type playerDTO struct {
	Color           game.Color `json:"color"`
	GemsRemaining   int        `json:"gemsRemaining"`
	CapturedPairs   int        `json:"capturedPairs"`
	TimeRemainingMs int64      `json:"timeRemainingMs"`
	// Alignment counts are deliberately not exposed: counting lines is part of
	// the game. Win detection still uses them server-side.
}

type gameDTO struct {
	ID         string        `json:"id"`
	Status     Status        `json:"status"`
	BoardSide  int           `json:"boardSide"`
	Cells      []game.Color  `json:"cells"`
	Players    []playerDTO   `json:"players"`
	Seats      []seatDTO     `json:"seats"`
	Turn       int           `json:"turn"`
	Winner     game.Color    `json:"winner"`
	WinKind    game.WinKind  `json:"winKind"`
	MoveCount  int           `json:"moveCount"`
	Thresholds thresholdsDTO `json:"thresholds"`
	// TurnStartedAt (RFC 3339) is when the active player's turn began; clients
	// run a live countdown against TimeRemainingMs. Empty before the game starts.
	TurnStartedAt string `json:"turnStartedAt,omitempty"`

	Visibility    Visibility `json:"visibility"`
	RematchGameID string     `json:"rematchGameId,omitempty"`

	// LastMove is the most recently placed stone, or nil if no moves yet, for
	// the "last move" ring during live play.
	LastMove *cellPosDTO `json:"lastMove,omitempty"`

	// RematchOffer is set while a rematch proposal is pending; nil once
	// RematchGameID is set (the new game takes precedence in the UI).
	RematchOffer *rematchOfferDTO `json:"rematchOffer,omitempty"`

	// DrawOfferBy is the offering seat index, or -1 for none. Only meaningful
	// while playing.
	DrawOfferBy int `json:"drawOfferBy"`
}

// rematchOfferDTO is the wire shape of a pending rematch. Arrays hold seat
// indices in the finished game; bots never appear.
type rematchOfferDTO struct {
	AcceptedSeats []int `json:"acceptedSeats"`
	PendingSeats  []int `json:"pendingSeats"`
}

// cellPosDTO is an axial coordinate wrapper, at package level so optional
// gameDTO fields can point to it.
type cellPosDTO struct {
	Q int `json:"q"`
	R int `json:"r"`
}

type thresholdsDTO struct {
	CapturePairsWin int   `json:"capturePairsWin"`
	Align4ToWin     int   `json:"align4ToWin"`
	Align5ToWin     int   `json:"align5ToWin"`
	InitialTimeMs   int64 `json:"initialTimeMs"`
	IncrementMs     int64 `json:"incrementMs"`
}

type captureDTO struct {
	Victim   game.Color `json:"victim"`
	Capturer game.Color `json:"capturer"`
	Pair     [2][2]int  `json:"pair"`
}

// replayStepDTO is one move in a replay; Captures lists pairs removed by this
// placement so the client can render the turn.
type replayStepDTO struct {
	Ordinal  int          `json:"ordinal"`
	Player   game.Color   `json:"player"`
	Q        int          `json:"q"`
	R        int          `json:"r"`
	Captures []captureDTO `json:"captures"`
}

type replayDTO struct {
	GameID    string          `json:"gameId"`
	BoardSide int             `json:"boardSide"`
	Players   int             `json:"players"`
	Steps     []replayStepDTO `json:"steps"`
}

type moveResponse struct {
	Game     gameDTO      `json:"game"`
	Captures []captureDTO `json:"captures"`
}

type joinResponse struct {
	Game  gameDTO `json:"game"`
	Seat  seatDTO `json:"seat"`
	Token string  `json:"token"`
}

func toGameDTO(rec *GameRecord) gameDTO {
	s := rec.State
	players := make([]playerDTO, len(s.Players))
	for i, p := range s.Players {
		players[i] = playerDTO{
			Color:           p.Color,
			GemsRemaining:   p.GemsRemaining,
			CapturedPairs:   p.CapturedPairs,
			TimeRemainingMs: p.TimeRemainingMs,
		}
	}
	// Route through toSeatDTO so new seatDTO fields aren't silently dropped
	// (inline construction here once omitted UserID everywhere but joins).
	seats := make([]seatDTO, len(rec.Seats))
	for i := range rec.Seats {
		seats[i] = toSeatDTO(&rec.Seats[i])
	}
	turnStartedAt := ""
	if !s.TurnStartedAt.IsZero() {
		turnStartedAt = s.TurnStartedAt.UTC().Format(time.RFC3339Nano)
	}
	vis := rec.Visibility
	if vis == "" {
		vis = VisibilityPrivate
	}
	// Win thresholds are decided at Start from the occupied count. While
	// waiting, preview them for the current count; once playing, s.Config wins.
	thr := s.Config
	if rec.Status == StatusWaiting {
		occupied := 0
		for _, st := range rec.Seats {
			if st.Occupied {
				occupied++
			}
		}
		if occupied < 2 {
			occupied = 2
		}
		thr = game.ConfigFor(occupied, s.Config)
	}
	// Copy cells: the DTO outlives the rec.Lock, so a later board mutation would
	// race json.Encoder over the shared backing array.
	cells := make([]game.Color, len(s.Board.Cells))
	copy(cells, s.Board.Cells)
	var lastMove *cellPosDTO
	if n := len(s.History); n > 0 {
		m := s.History[n-1]
		lastMove = &cellPosDTO{Q: m.Pos.Q, R: m.Pos.R}
	}
	return gameDTO{
		ID:            rec.ID,
		Status:        rec.Status,
		BoardSide:     s.Board.Side,
		Cells:         cells,
		TurnStartedAt: turnStartedAt,
		Players:       players,
		Seats:         seats,
		Turn:          s.Turn,
		Winner:        s.Winner,
		WinKind:       s.WinKind,
		MoveCount:     len(s.History),
		Visibility:    vis,
		RematchGameID: rec.RematchGameID,
		LastMove:      lastMove,
		RematchOffer:  toRematchOfferDTO(rec),
		DrawOfferBy:   rec.DrawOfferBy,
		Thresholds: thresholdsDTO{
			CapturePairsWin: thr.CapturePairsWin,
			Align4ToWin:     thr.Align4ToWin,
			Align5ToWin:     thr.Align5ToWin,
			InitialTimeMs:   thr.InitialTimeMs,
			IncrementMs:     thr.IncrementMs,
		},
	}
}

// toRematchOfferDTO projects rec.RematchOffer to the wire shape, nil once a
// rematch game exists. Caller must hold rec's lock.
func toRematchOfferDTO(rec *GameRecord) *rematchOfferDTO {
	if rec.RematchOffer == nil || rec.RematchGameID != "" {
		return nil
	}
	var accepted, pending []int
	for i, st := range rec.Seats {
		if !st.Occupied || st.IsBot {
			continue
		}
		if rec.RematchOffer.AcceptedSeats[i] {
			accepted = append(accepted, i)
		} else {
			pending = append(pending, i)
		}
	}
	if accepted == nil {
		accepted = []int{}
	}
	if pending == nil {
		pending = []int{}
	}
	return &rematchOfferDTO{AcceptedSeats: accepted, PendingSeats: pending}
}

func toSeatDTO(s *Seat) seatDTO {
	return seatDTO{
		Index:    s.Index,
		Color:    s.Color,
		Name:     s.Name,
		UserID:   s.UserID,
		Occupied: s.Occupied,
		IsBot:    s.IsBot,
	}
}

func toCaptureDTOs(in []game.Capture) []captureDTO {
	out := make([]captureDTO, len(in))
	for i, c := range in {
		out[i] = captureDTO{
			Victim:   c.Victim,
			Capturer: c.Capturer,
			Pair: [2][2]int{
				{c.Pair[0].Q, c.Pair[0].R},
				{c.Pair[1].Q, c.Pair[1].R},
			},
		}
	}
	return out
}
