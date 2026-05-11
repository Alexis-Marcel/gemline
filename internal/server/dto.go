package server

import "github.com/alexis/gemline/internal/game"

type createGameRequest struct {
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
	Index    int        `json:"index"`
	Color    game.Color `json:"color"`
	Name     string     `json:"name"`
	Occupied bool       `json:"occupied"`
	IsBot    bool       `json:"isBot"`
}

type playerDTO struct {
	Color         game.Color `json:"color"`
	GemsRemaining int        `json:"gemsRemaining"`
	CapturedPairs int        `json:"capturedPairs"`
	// Alignment counts are deliberately not exposed: counting your own and
	// your opponents' lines is part of the game. Win detection still uses
	// them server-side, and the WinKind field reveals how a finished game
	// was decided.
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
}

type thresholdsDTO struct {
	CapturePairsWin int `json:"capturePairsWin"`
	Align4ToWin     int `json:"align4ToWin"`
	Align5ToWin     int `json:"align5ToWin"`
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
			Color:         p.Color,
			GemsRemaining: p.GemsRemaining,
			CapturedPairs: p.CapturedPairs,
		}
	}
	seats := make([]seatDTO, len(rec.Seats))
	for i, st := range rec.Seats {
		seats[i] = seatDTO{
			Index:    st.Index,
			Color:    st.Color,
			Name:     st.Name,
			Occupied: st.Occupied,
			IsBot:    st.IsBot,
		}
	}
	return gameDTO{
		ID:        rec.ID,
		Status:    rec.Status,
		BoardSide: s.Board.Side,
		Cells:     s.Board.Cells,
		Players:   players,
		Seats:     seats,
		Turn:      s.Turn,
		Winner:    s.Winner,
		WinKind:   s.WinKind,
		MoveCount: len(s.History),
		Thresholds: thresholdsDTO{
			CapturePairsWin: s.Config.CapturePairsWin,
			Align4ToWin:     s.Config.Align4ToWin,
			Align5ToWin:     s.Config.Align5ToWin,
		},
	}
}

func toSeatDTO(s *Seat) seatDTO {
	return seatDTO{
		Index:    s.Index,
		Color:    s.Color,
		Name:     s.Name,
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
