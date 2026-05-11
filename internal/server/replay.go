package server

import (
	"net/http"

	"github.com/alexis/gemline/internal/game"
)

// getReplay returns the full move-by-move replay of a game. It works on any
// game (in progress or finished) and re-runs every move through a fresh
// rule engine to surface captures per step.
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

	// Replay against a fresh GameState so we can capture per-move MoveResults
	// without touching the cached record.
	replay := game.NewGame(colors, cfg)
	steps := make([]replayStepDTO, 0, len(history))
	for i, m := range history {
		res, err := replay.ApplyMove(m)
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
