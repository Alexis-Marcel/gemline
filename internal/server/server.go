package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/alexis/gemline/internal/game"
	"github.com/golang-jwt/jwt/v5"
)

type Server struct {
	store    *Store
	hub      *Hub
	log      *slog.Logger
	verifier jwt.Keyfunc
}

// Config holds optional dependencies that change how the server behaves.
//
// When SupabaseURL is set, the server fetches Supabase's JWKS document
// from <SupabaseURL>/auth/v1/.well-known/jwks.json and verifies
// asymmetrically-signed user JWTs. When it's empty, auth is disabled
// and every /api/auth/* endpoint responds 401.
type Config struct {
	SupabaseURL string
}

// New returns a Server backed by the given store and config.
func New(log *slog.Logger, store *Store, cfg Config) *Server {
	srv := &Server{
		store: store,
		hub:   NewHub(),
		log:   log,
	}

	if cfg.SupabaseURL == "" {
		log.Warn("auth disabled — set SUPABASE_URL to enable")
	} else if kf, err := jwksKeyfunc(cfg.SupabaseURL); err != nil {
		log.Error("could not initialise JWKS verifier", "err", err)
	} else {
		srv.verifier = kf
		log.Info("auth enabled", "scheme", "jwks", "url", cfg.SupabaseURL)
	}
	// When a player's clock runs out or their disconnect grace expires,
	// push the new state to every WS subscriber so they see the forfeit.
	store.SetStateListener(func(gameID string) {
		rec, ok, err := store.Get(context.Background(), gameID)
		if err != nil || !ok {
			return
		}
		rec.Lock()
		dto := toGameDTO(rec)
		rec.Unlock()
		srv.hub.Broadcast(gameID, eventState(dto))
	})
	// Presence changes (a seat just went online or offline) are broadcast
	// as a lightweight `presence` event so the UI can flip the badge
	// without a full state push.
	store.SetPresenceListener(func(gameID string, seatIndex int, online bool) {
		srv.hub.Broadcast(gameID, eventPresence(seatIndex, online))
	})
	// Draw-offer transitions are infrequent and the change shows up on the
	// game DTO (drawOfferBy field), so we just push a full state snapshot
	// rather than introducing a dedicated wire event.
	store.SetDrawOfferListener(func(gameID string, _ int) {
		rec, ok, err := store.Get(context.Background(), gameID)
		if err != nil || !ok {
			return
		}
		rec.Lock()
		dto := toGameDTO(rec)
		rec.Unlock()
		srv.hub.Broadcast(gameID, eventState(dto))
	})
	// Server-driven moves (bots) broadcast a move event with the same shape
	// HTTP-driven moves use, so clients render captures + the new state
	// identically whether the move came from a human or a bot.
	store.SetMoveListener(func(gameID string, mv game.MoveResult) {
		rec, ok, err := store.Get(context.Background(), gameID)
		if err != nil || !ok {
			return
		}
		rec.Lock()
		dto := toGameDTO(rec)
		rec.Unlock()
		resp := moveResponse{Game: dto, Captures: toCaptureDTOs(mv.Captures)}
		srv.hub.Broadcast(gameID, eventMove(resp))
	})
	return srv
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("POST /api/games", s.createGame)
	mux.HandleFunc("POST /api/games/matchmake", s.matchmakeGame)
	mux.HandleFunc("GET /api/games/{id}", s.getGame)
	mux.HandleFunc("POST /api/games/{id}/join", s.joinGame)
	mux.HandleFunc("POST /api/games/{id}/moves", s.postMove)
	mux.HandleFunc("POST /api/games/{id}/resign", s.resignGame)
	mux.HandleFunc("POST /api/games/{id}/draw/offer", s.offerDraw)
	mux.HandleFunc("POST /api/games/{id}/draw/accept", s.acceptDraw)
	mux.HandleFunc("POST /api/games/{id}/draw/decline", s.declineDraw)
	mux.HandleFunc("POST /api/games/{id}/rematch", s.rematchGame)
	mux.HandleFunc("POST /api/games/{id}/seats/{idx}/bot", s.addBot)
	mux.HandleFunc("GET /api/games/{id}/replay", s.getReplay)
	mux.HandleFunc("GET /api/games/{id}/messages", s.getChat)
	mux.HandleFunc("POST /api/games/{id}/messages", s.postChat)
	mux.HandleFunc("GET /ws/games/{id}", s.wsGame)

	mux.HandleFunc("GET /api/auth/me", s.getMe)
	mux.HandleFunc("PUT /api/profile", s.putProfile)
	mux.HandleFunc("GET /api/users/me/games", s.getMyGames)
	mux.HandleFunc("GET /api/users/me/stats", s.getMyStats)

	return loggingMiddleware(s.log, corsMiddleware(jwtMiddleware(s.verifier, mux)))
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
	vis := Visibility(req.Visibility)
	if vis == "" {
		vis = VisibilityPrivate
	}
	if vis != VisibilityPrivate && vis != VisibilityPublic {
		writeError(w, http.StatusBadRequest, "visibility must be 'public' or 'private'")
		return
	}
	rec, err := s.store.Create(r.Context(), req.Players, vis)
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

// matchmakeGame returns the public waiting game the caller should join — an
// existing one with a free seat (oldest first), or a freshly-created public
// game if no candidate is matchable. The client follows up with /join to
// actually claim a seat.
func (s *Server) matchmakeGame(w http.ResponseWriter, r *http.Request) {
	var req matchmakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Players < 2 || req.Players > game.MaxPlayers {
		writeError(w, http.StatusBadRequest, "players must be in [2, 6]")
		return
	}
	rec, err := s.store.Matchmake(r.Context(), req.Players)
	if err != nil {
		s.log.Error("matchmake", "err", err)
		writeError(w, http.StatusInternalServerError, "could not matchmake")
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	writeJSON(w, http.StatusOK, dto)
}

// addBot fills the requested empty seat with a bot. Only private waiting
// games accept bots; matchmade public games must be filled by humans.
func (s *Server) addBot(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	seatIdx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid seat index")
		return
	}
	rec, err := s.store.AddBot(r.Context(), gameID, seatIdx)
	if err != nil {
		writeError(w, statusForAddBotError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.hub.Broadcast(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

func statusForAddBotError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadSeatIndex):
		return http.StatusBadRequest
	case errors.Is(err, ErrSeatTaken),
		errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrBotsOnPublic):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) rematchGame(w http.ResponseWriter, r *http.Request) {
	originalID := r.PathValue("id")
	rec, err := s.store.Rematch(r.Context(), originalID)
	if err != nil {
		writeError(w, statusForRematchError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	writeJSON(w, http.StatusCreated, rematchResponse{GameID: rec.ID, Game: dto})
}

// resignGame, offerDraw, acceptDraw, declineDraw all share the same shape:
// extract the seat token, hand off to a Store method, broadcast the new state
// on success. The differences are which method to call and what status codes
// the errors map to.

func (s *Server) resignGame(w http.ResponseWriter, r *http.Request) {
	s.endByConcession(w, r, s.store.Resign, "resign")
}

func (s *Server) offerDraw(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := s.store.OfferDraw(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForDrawError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) acceptDraw(w http.ResponseWriter, r *http.Request) {
	s.endByConcession(w, r, s.store.AcceptDraw, "draw_accept")
}

func (s *Server) declineDraw(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := s.store.DeclineDraw(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForDrawError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	writeJSON(w, http.StatusOK, dto)
}

// endByConcession is the shared body of resign + accept-draw — they both
// auth via seat token, end the game, and broadcast a state snapshot.
func (s *Server) endByConcession(
	w http.ResponseWriter,
	r *http.Request,
	fn func(ctx context.Context, gameID, token string) (*GameRecord, error),
	op string,
) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := fn(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForConcessionError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.hub.Broadcast(gameID, eventState(dto))
	s.log.Info("game ended", "op", op, "game", gameID, "winner", dto.Winner, "kind", dto.WinKind)
	writeJSON(w, http.StatusOK, dto)
}

func statusForConcessionError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNotPlaying):
		return http.StatusConflict
	case errors.Is(err, ErrDrawNotOffered), errors.Is(err, ErrCannotAcceptOwnDrawOffer):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func statusForDrawError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrDrawAlreadyOffered),
		errors.Is(err, ErrDrawNotOffered),
		errors.Is(err, ErrDrawUnsupported):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func statusForRematchError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNotFinished):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
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

	userID := ""
	if u, ok := userFromContext(r.Context()); ok {
		userID = u.ID
	}
	seat, token, err := s.store.Join(r.Context(), gameID, req.Name, userID, seatIdx)
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
	token := playerToken(r)
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
