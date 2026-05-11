package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alexis/gemline/internal/game"
)

type Server struct {
	store *Store
	hub   *Hub
	log   *slog.Logger
}

func New(log *slog.Logger) *Server {
	return &Server{
		store: NewStore(),
		hub:   NewHub(),
		log:   log,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /api/games", s.createGame)
	mux.HandleFunc("GET /api/games/{id}", s.getGame)
	mux.HandleFunc("POST /api/games/{id}/join", s.joinGame)
	mux.HandleFunc("POST /api/games/{id}/moves", s.postMove)
	mux.HandleFunc("GET /ws/games/{id}", s.wsGame)
	return loggingMiddleware(s.log, corsMiddleware(mux))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) createGame(w http.ResponseWriter, r *http.Request) {
	var req createGameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Players < 2 || req.Players > game.MaxPlayers {
		writeError(w, http.StatusBadRequest, "players must be in [2, 6]")
		return
	}
	rec := s.store.Create(req.Players)
	writeJSON(w, http.StatusCreated, toGameDTO(rec))
}

func (s *Server) getGame(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) joinGame(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}
	var req joinGameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	seatIdx := -1
	if req.Seat != nil {
		seatIdx = *req.Seat
	}
	rec.Lock()
	seat, err := Join(rec, req.Name, seatIdx)
	var resp joinResponse
	if err == nil {
		resp = joinResponse{
			Game:  toGameDTO(rec),
			Seat:  toSeatDTO(seat),
			Token: seat.Token,
		}
	}
	rec.Unlock()
	if err != nil {
		writeError(w, statusForJoinError(err), err.Error())
		return
	}
	s.hub.Broadcast(rec.ID, eventState(resp.Game))
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) postMove(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	var req moveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	rec.Lock()
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		writeError(w, http.StatusConflict, "game is not playing")
		return
	}
	out, err := rec.State.ApplyMove(game.Move{
		Player: seat.Color,
		Pos:    game.Position{Q: req.Q, R: req.R},
	})
	if err != nil {
		rec.Unlock()
		writeError(w, statusForMoveError(err), err.Error())
		return
	}
	if rec.State.Winner != game.Empty {
		rec.Status = StatusFinished
	}
	resp := moveResponse{
		Game:     toGameDTO(rec),
		Captures: toCaptureDTOs(out.Captures),
	}
	rec.Unlock()

	s.hub.Broadcast(rec.ID, eventMove(resp))
	writeJSON(w, http.StatusOK, resp)
}

func statusForJoinError(err error) int {
	switch {
	case errors.Is(err, ErrSeatTaken), errors.Is(err, ErrNoFreeSeat):
		return http.StatusConflict
	case errors.Is(err, ErrNotPlaying):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func statusForMoveError(err error) int {
	switch {
	case errors.Is(err, game.ErrOutOfBounds),
		errors.Is(err, game.ErrCellOccupied),
		errors.Is(err, game.ErrWrongTurn),
		errors.Is(err, game.ErrNoGemsLeft):
		return http.StatusBadRequest
	case errors.Is(err, game.ErrGameOver):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if t := r.Header.Get("X-Player-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
