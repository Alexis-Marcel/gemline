package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/alexis/gemline/internal/backplane"
	"github.com/alexis/gemline/internal/game"
	"github.com/golang-jwt/jwt/v5"
)

type Server struct {
	store     *Store
	hub       *Hub
	lobby     *Hub // keyed by userID; same Hub type, different routing key semantics
	events    *EventPublisher
	backplane *backplane.Backplane
	log       *slog.Logger
	verifier  jwt.Keyfunc
	// nil/empty = dev-permissive (CORS `*`, WS skips origin).
	allowedOrigins []string
}

// Config holds optional dependencies that change how the server behaves.
//
// When SupabaseURL is set, the server fetches Supabase's JWKS document
// from <SupabaseURL>/auth/v1/.well-known/jwks.json and verifies
// asymmetrically-signed user JWTs. When it's empty, auth is disabled
// and every /api/auth/* endpoint responds 401.
//
// AllowedOrigins controls which Origins are permitted by the CORS middleware
// and the WebSocket upgrade. Empty/unset means "dev mode" — `*` for CORS and
// the WS origin check is skipped. Production deployments MUST set this to
// the actual frontend origin(s).
type Config struct {
	SupabaseURL    string
	AllowedOrigins []string
}

// New returns a Server backed by the given store and config. The bp
// argument is the Postgres backplane used to fan game events across
// pods; pass nil for single-process runs (tests, no DATABASE_URL) and
// EventPublisher will fall back to direct local delivery.
func New(log *slog.Logger, store *Store, bp *backplane.Backplane, cfg Config) *Server {
	hub := NewHub(log)
	lobby := NewHub(log)
	podID := newPodID()
	log.Info("server starting", "pod_id", podID)
	srv := &Server{
		store:          store,
		hub:            hub,
		lobby:          lobby,
		events:         NewEventPublisher(store.Repo(), hub, bp, log, podID, store.Invalidate),
		backplane:      bp,
		log:            log,
		allowedOrigins: cfg.AllowedOrigins,
	}
	// When a backplane is present, register the listener handlers so
	// notifications coming in from any pod (including ours) get fanned
	// out to local WS subscribers. Two channels: game events and lobby
	// match notifications.
	if bp != nil {
		bp.Subscribe(ChannelGameEvents, srv.events.HandleGameEventNotif)
		bp.Subscribe(ChannelLobby, srv.handleLobbyNotif)
	}
	if len(cfg.AllowedOrigins) == 0 {
		log.Warn("CORS + WS origin checks disabled — set AllowedOrigins for production")
	} else {
		log.Info("CORS + WS origin checks enabled", "origins", cfg.AllowedOrigins)
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
		srv.events.Publish(gameID, eventState(dto))
	})
	// Presence changes (a seat just went online or offline) are broadcast
	// as a lightweight `presence` event so the UI can flip the badge
	// without a full state push.
	store.SetPresenceListener(func(gameID string, seatIndex int, online bool) {
		srv.events.Publish(gameID, eventPresence(seatIndex, online))
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
		srv.events.Publish(gameID, eventState(dto))
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
		srv.events.Publish(gameID, eventMove(resp))
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
	mux.HandleFunc("POST /api/games/{id}/leave", s.leaveSeat)
	mux.HandleFunc("POST /api/games/{id}/start", s.startGame)
	mux.HandleFunc("GET /api/games/{id}/replay", s.getReplay)
	mux.HandleFunc("GET /api/games/{id}/events", s.getGameEvents)
	mux.HandleFunc("GET /api/games/{id}/messages", s.getChat)
	mux.HandleFunc("POST /api/games/{id}/messages", s.postChat)
	mux.HandleFunc("GET /ws/games/{id}", s.wsGame)
	mux.HandleFunc("GET /ws/lobby", s.wsLobby)

	mux.HandleFunc("POST /api/matchmake/enqueue", s.enqueueMatchmake)
	mux.HandleFunc("DELETE /api/matchmake/enqueue", s.cancelMatchmake)

	mux.HandleFunc("GET /api/auth/me", s.getMe)
	mux.HandleFunc("PUT /api/profile", s.putProfile)
	mux.HandleFunc("GET /api/users/me/games", s.getMyGames)
	mux.HandleFunc("GET /api/users/me/stats", s.getMyStats)
	mux.HandleFunc("GET /api/leaderboard", s.getLeaderboard)

	return loggingMiddleware(s.log, corsMiddleware(s.allowedOrigins, jwtMiddleware(s.verifier, mux)))
}

// StartMatcher kicks off the background matchmaker ticker on this pod.
// Every matcherTickInterval each supported player count is processed
// via SELECT FOR UPDATE SKIP LOCKED, so multiple pods can run their
// own matcher in parallel without coordination. Match results are
// fanned out via the lobby channel so each user's lobby WS (which may
// live on a different pod) receives their match_found event.
//
// Cancel via ctx. Safe to call without a backplane (single-process /
// no-DB mode): the matcher still runs but onMatched falls back to the
// local LobbyHub.Deliver instead of NOTIFYing.
func (s *Server) StartMatcher(ctx context.Context) {
	s.store.StartMatcher(ctx, s.log, s.fanMatched)
}

// newPodID returns a process-unique identifier used to tag outgoing
// NOTIFY envelopes so receiving pods can distinguish self-originated
// events from genuine cross-pod ones.
//
// Hostname + a short random suffix is enough: the hostname is stable
// per K8s pod and tells you which one you're looking at in logs, the
// suffix makes the value unique across restarts (so if a pod restarts
// with the same hostname its in-flight notifications don't get
// silently absorbed). Falls back to pure random if hostname lookup
// somehow fails.
func newPodID() string {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	suffix := hex.EncodeToString(rnd[:])
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "pod-" + suffix
	}
	return host + "-" + suffix
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// createGame creates a private game (visibility=private only; public games
// only come from matchmaking) and atomically auto-joins the caller so the
// client lands on the game already seated. Authenticated users have their
// display name pulled from the profile; anonymous users must supply a name
// in the request body. We won't ask them to retype it on the game page.
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
	// Only private games are creatable this way — public games are created
	// implicitly by Store.Matchmake when no public candidate exists.
	if vis != VisibilityPrivate {
		writeError(w, http.StatusBadRequest, "only private games are creatable directly; use /matchmake for public")
		return
	}

	// Resolve the host's display name. Auth → profile (always available
	// via displayNameFor's fallbacks). Anonymous → required from body.
	userID := ""
	name := strings.TrimSpace(req.Name)
	if u, ok := userFromContext(r.Context()); ok {
		userID = u.ID
		if name == "" {
			name = s.displayNameFor(r.Context(), u)
		}
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required for anonymous create")
		return
	}

	rec, err := s.store.Create(r.Context(), req.Players, vis)
	if err != nil {
		s.log.Error("create game", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create game")
		return
	}
	seat, token, err := s.store.Join(r.Context(), rec.ID, name, userID, 0)
	if err != nil {
		s.log.Error("create-join", "err", err)
		writeError(w, statusForJoinError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(rec.ID, eventState(dto))
	writeJSON(w, http.StatusCreated, joinResponse{Game: dto, Seat: toSeatDTO(seat), Token: token})
}

// matchmakeGame returns the public waiting game the caller should join — an
// existing one with a free seat (oldest first), or a freshly-created public
// game if no candidate is matchable. Atomically auto-joins the caller so
// the client can navigate straight into the game without a follow-up /join.
// Requires authentication — matchmade games feed the rating system, and
// rating needs a stable identity.
func (s *Server) matchmakeGame(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var req matchmakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Players < 2 || req.Players > game.MaxPlayers {
		writeError(w, http.StatusBadRequest, "players must be in [2, 6]")
		return
	}
	rec, err := s.store.Matchmake(r.Context(), req.Players, u.ID)
	if err != nil {
		s.log.Error("matchmake", "err", err)
		writeError(w, http.StatusInternalServerError, "could not matchmake")
		return
	}
	name := s.displayNameFor(r.Context(), u)
	seat, token, err := s.store.Join(r.Context(), rec.ID, name, u.ID, -1)
	if err != nil {
		writeError(w, statusForJoinError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(rec.ID, eventState(dto))
	writeJSON(w, http.StatusOK, joinResponse{Game: dto, Seat: toSeatDTO(seat), Token: token})
}

// displayNameFor resolves the user's preferred display name, falling back to
// the email's local-part and finally to a generic "Joueur" if everything is
// blank. Used by paths that auto-join (matchmake, signed-in join without a
// body) — never asks the user to retype something we already know.
func (s *Server) displayNameFor(ctx context.Context, u *AuthUser) string {
	if p, err := s.store.Profile(ctx, u.ID); err == nil && p != nil && p.DisplayName != "" {
		return p.DisplayName
	}
	if at := strings.IndexByte(u.Email, '@'); at > 0 {
		return u.Email[:at]
	}
	if u.Email != "" {
		return u.Email
	}
	return "Joueur"
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
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

// startGame finalises a private game (fill empty seats with bots, flip to
// playing). Authentication is via the seat token — any participant can
// kick off the start in a private game.
func (s *Server) startGame(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := s.store.Start(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForStartError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

func statusForStartError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrPublicCannotStart),
		errors.Is(err, ErrTooFewToStart):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// leaveSeat frees the caller's seat in a still-waiting game. Equivalent to
// "cancel matchmaking" from the user's perspective: they vacate the seat
// they were holding and the game becomes joinable again.
func (s *Server) leaveSeat(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := s.store.LeaveSeat(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForLeaveError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

func statusForLeaveError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNotPlaying):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
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
	s.events.Publish(gameID, eventState(dto))
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

// catchupLimit caps how many events one /events call can return. A
// client that's been disconnected for hours could otherwise pull
// thousands of rows in a single request. 1000 covers a generously long
// game (a 6P session might rack up ~200-400 events end-to-end) without
// letting a hostile or buggy client DoS the DB.
const catchupLimit = 1000

// getGameEvents serves the WebSocket catch-up endpoint. Clients call it
// on reconnect with their last-seen seq; the server returns every event
// with seq > since, in ascending order. A fresh connect uses since=0
// (or omits the parameter) and gets the full event log — usually the
// caller will rely on the connect-time state snapshot instead.
//
// No authentication is required beyond what /api/games/:id already
// expects: any game ID known to the caller is already exposed by /get,
// so /events on the same ID surfaces nothing new in terms of access.
func (s *Server) getGameEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since := 0
	if raw := r.URL.Query().Get("since"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "since must be a non-negative integer")
			return
		}
		since = n
	}
	rows, err := s.store.Repo().EventsSince(r.Context(), id, since, catchupLimit)
	if err != nil {
		s.log.Error("events since", "game", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load events")
		return
	}
	if rows == nil {
		// json.Marshal turns a nil slice into "null", which clients
		// would have to special-case. Always emit "[]" on the wire.
		rows = []EventRow{}
	}
	writeJSON(w, http.StatusOK, rows)
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
	// The body is optional for authenticated users (we know their identity
	// and display name). Anonymous joins still need a name, but the absence
	// of a body should not blow up the request.
	var req joinGameRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	seatIdx := -1
	if req.Seat != nil {
		seatIdx = *req.Seat
	}

	userID := ""
	name := strings.TrimSpace(req.Name)
	if u, ok := userFromContext(r.Context()); ok {
		userID = u.ID
		if name == "" {
			name = s.displayNameFor(r.Context(), u)
		}
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required for anonymous join")
		return
	}
	seat, token, err := s.store.Join(r.Context(), gameID, name, userID, seatIdx)
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
	s.events.Publish(gameID, eventState(dto))
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
	s.events.Publish(gameID, eventMove(resp))
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
	case errors.Is(err, ErrAnonymousOnPublic):
		return http.StatusUnauthorized
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
