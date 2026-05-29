package server

import (
	"net/http"
	"time"

	"github.com/alexis/gemline/internal/game"
)

// getReplay re-runs every move of a game through a fresh engine to surface
// captures per step. Works on in-progress or finished games.
func (s *Server) getReplay(w http.ResponseWriter, r *http.Request) {
	rec, ok, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.log.Error("replay load", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load game")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}

	rec.Lock()
	cfg := rec.State.Config
	history := append([]game.Move{}, rec.State.History...)
	colors := make([]game.Color, len(rec.State.Players))
	for i, p := range rec.State.Players {
		colors[i] = p.Color
	}
	rec.Unlock()

	// Replay against a fresh GameState; clock progression is irrelevant here,
	// so a zero `now` is fine.
	replay := game.NewGame(colors, cfg)
	steps := make([]replayStepDTO, 0, len(history))
	var zeroTime time.Time
	for i, m := range history {
		res, err := replay.ApplyMove(m, zeroTime)
		if err != nil {
			s.log.Error("replay apply", "err", err, "ordinal", i)
			writeError(w, http.StatusInternalServerError, "replay failed")
			return
		}
		steps = append(steps, replayStepDTO{
			Ordinal:  i,
			Player:   m.Player,
			Q:        m.Pos.Q,
			R:        m.Pos.R,
			Captures: toCaptureDTOs(res.Captures),
		})
	}

	writeJSON(w, http.StatusOK, replayDTO{
		GameID:    rec.ID,
		BoardSide: cfg.BoardSide,
		Players:   len(colors),
		Steps:     steps,
	})
}
