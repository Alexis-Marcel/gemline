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

// matchmakeRequest is the body of POST /api/games/matchmake. The frontend
// exposes only the two semantic flavours (1v1 → players=2, multijoueur →
// players=4) but the API stays open to any 2..6 value for future tweaking.
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
	// UserID is the public Supabase user id for authenticated seats.
	// Empty for anonymous players and bots. Surfaced so the frontend
	// can link a seat's name to that player's public profile page
	// without having to cross-reference the ratings payload.
	UserID   string `json:"userId,omitempty"`
	Occupied bool   `json:"occupied"`
	IsBot    bool   `json:"isBot"`
}

type playerDTO struct {
	Color           game.Color `json:"color"`
	GemsRemaining   int        `json:"gemsRemaining"`
	CapturedPairs   int        `json:"capturedPairs"`
	TimeRemainingMs int64      `json:"timeRemainingMs"`
	// Alignment counts are deliberately not exposed: counting your own and
	// your opponents' lines is part of the game. Win detection still uses
	// them server-side, and the WinKind field reveals how a finished game
	// was decided.
}

type gameDTO struct {
	ID            string        `json:"id"`
	Status        Status        `json:"status"`
	BoardSide     int           `json:"boardSide"`
	Cells         []game.Color  `json:"cells"`
	Players       []playerDTO   `json:"players"`
	Seats         []seatDTO     `json:"seats"`
	Turn          int           `json:"turn"`
	Winner        game.Color    `json:"winner"`
	WinKind       game.WinKind  `json:"winKind"`
	MoveCount     int           `json:"moveCount"`
	Thresholds    thresholdsDTO `json:"thresholds"`
	// TurnStartedAt is the server's wall clock when the active player's
	// turn began (RFC 3339). Clients use it to display a live countdown
	// against the active player's TimeRemainingMs. Empty for not-yet-started
	// games (status = "waiting").
	TurnStartedAt string `json:"turnStartedAt,omitempty"`

	Visibility    Visibility `json:"visibility"`
	RematchGameID string     `json:"rematchGameId,omitempty"`

	// LastMove is the axial coordinate of the most recently placed stone,
	// or nil for a game that has had no moves yet. Surfaced so the client
	// can paint a chess.com-style "last move" ring on the board during
	// live play (replay mode computes its own indicator from the step).
	LastMove *cellPosDTO `json:"lastMove,omitempty"`

	// RematchOffer is set on a finished game while a rematch proposal is
	// pending. Nil when no offer is active and once RematchGameID is set
	// (the new game then takes precedence in the UI).
	RematchOffer *rematchOfferDTO `json:"rematchOffer,omitempty"`

	// DrawOfferBy is the seat index that currently has a draw offer
	// pending, or -1 when no offer is active. Only meaningful while
	// status == "playing".
	DrawOfferBy int `json:"drawOfferBy"`
}

// rematchOfferDTO is the wire shape of a pending rematch proposal. The
// arrays are seat indices in the *finished* game (not the rematch). Bots
// never appear in either array — they're invisible to the acceptance flow.
type rematchOfferDTO struct {
	// AcceptedSeats lists human seats that have already accepted (including
	// the proposer, who is just "first to accept").
	AcceptedSeats []int `json:"acceptedSeats"`
	// PendingSeats lists human seats whose acceptance is still required for
	// the rematch to be created. Empty means the offer is about to resolve.
	PendingSeats []int `json:"pendingSeats"`
}

// cellPosDTO is a tiny axial coordinate wrapper. Lives at the package
// level so optional gameDTO fields can carry a pointer to it.
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

// replayStepDTO is one entry in the move-by-move replay of a finished game.
// captures lists pairs that were removed by this very placement, so a client
// can render exactly what happened on this turn.
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
	// Route through toSeatDTO so this stays in sync when seatDTO grows
	// fields. The inline construction that used to live here dropped
	// every field added after the original five, including UserID —
	// that's how PR #20's userId surfacing silently failed everywhere
	// except the join responses.
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
	// Win conditions are decided at Start time based on how many seats are
	// actually occupied (cf. ConfigFor / startInternal). While the game is
	// still in `waiting`, surface a *preview* of the thresholds for the
	// current occupied count so the host sees the rules they're about to
	// commit to. Once status flips to playing, s.Config is authoritative.
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
	// Defensive copy of the cells: the dto outlives the rec.Lock scope, so
	// after the caller Unlocks any subsequent board mutation (e.g. a bot
	// move) would race with json.Encoder iterating dto.Cells. Holding the
	// slice header by value isn't enough — the backing array is shared.
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

// toRematchOfferDTO projects rec.RematchOffer onto the wire shape. Returns
// nil when there's no offer or once a rematch game has been created (the
// frontend reads rematchGameId in that case). Caller must hold rec's lock.
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
