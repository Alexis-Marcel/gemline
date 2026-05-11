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

// New returns a Server backed by the given store. Callers wire the store with
// the desired Repository (Postgres in production, noopRepo for tests).
func New(log *slog.Logger, store *Store) *Server {
	return &Server{store: store, hub: NewHub(), log: log}
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
	rec, err := s.store.Create(r.Context(), req.Players)
	if err != nil {
		s.log.Error("create game", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create game")
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) getGame(w http.ResponseWriter, r *http.Request) {
	rec, ok, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.log.Error("get game", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load game")
		return
	}
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
	gameID := r.PathValue("id")
	var req joinGameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	seatIdx := -1
	if req.Seat != nil {
		seatIdx = *req.Seat
	}

	seat, token, err := s.store.Join(r.Context(), gameID, req.Name, seatIdx)
	if err != nil {
		writeError(w, statusForJoinError(err), err.Error())
		return
	}

	// Snapshot the game for the response and the broadcast.
	rec, _, _ := s.store.Get(r.Context(), gameID)
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()

	resp := joinResponse{Game: dto, Seat: toSeatDTO(seat), Token: token}
	s.hub.Broadcast(gameID, eventState(dto))
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) postMove(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
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

	out, rec, err := s.store.PlayMove(r.Context(), gameID, token, req.Q, req.R)
	if err != nil {
		writeError(w, statusForMoveError(err), err.Error())
		return
	}

	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()

	resp := moveResponse{Game: dto, Captures: toCaptureDTOs(out.Captures)}
	s.hub.Broadcast(gameID, eventMove(resp))
	writeJSON(w, http.StatusOK, resp)
}

func statusForJoinError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
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
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNotPlaying):
		return http.StatusConflict
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
