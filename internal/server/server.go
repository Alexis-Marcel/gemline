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
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

// Config holds optional dependencies. Empty SupabaseURL disables auth (every
// /api/auth/* responds 401). Empty AllowedOrigins is dev mode (CORS "*" and the
// WS origin check skipped); production must set the real frontend origin(s).
type Config struct {
	SupabaseURL    string
	AllowedOrigins []string
}

// New returns a Server. bp is the Postgres backplane for cross-pod event
// fan-out; nil (tests, no DATABASE_URL) falls back to direct local delivery.
func New(log *slog.Logger, store *Store, bp *backplane.Backplane, cfg Config) *Server {
	hub := NewHub(log, "game")
	lobby := NewHub(log, "lobby")
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
	// Register listener handlers so NOTIFYs from any pod (including ours) fan
	// out to local WS subscribers: game events + lobby match notifications.
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
	// Clock flag / disconnect-grace expiry: push fresh state so subscribers
	// see the forfeit.
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
	// Presence flips broadcast a lightweight `presence` event instead of a full
	// state push.
	store.SetPresenceListener(func(gameID string, seatIndex int, online bool) {
		srv.events.Publish(gameID, eventPresence(seatIndex, online))
	})
	// Draw-offer transitions show up on the DTO (drawOfferBy), so a full state
	// snapshot is enough — no dedicated wire event.
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
	// Bot moves broadcast the same move event shape as human moves so clients
	// render captures + state identically.
	store.SetMoveListener(func(gameID string, mv game.MoveResult) {
		movesPlayedTotal.WithLabelValues("bot").Inc()
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
	// Fires once per rated game after ApplyRatedGame commits. Rebuild the
	// snapshot from the DB (not deltas) so the WS event and the HTTP catch-up
	// share one shape.
	store.SetRatedListener(func(gameID string) {
		gr, err := store.RatingsForGame(context.Background(), gameID)
		if err != nil {
			log.Error("rated listener: load ratings", "game", gameID, "err", err)
			return
		}
		srv.events.Publish(gameID, eventRated(gr))
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
	mux.HandleFunc("POST /api/games/{id}/rematch/offer", s.offerRematch)
	mux.HandleFunc("POST /api/games/{id}/rematch/decline", s.declineRematch)
	mux.HandleFunc("POST /api/games/{id}/seats/{idx}/bot", s.addBot)
	mux.HandleFunc("DELETE /api/games/{id}/seats/{idx}/bot", s.removeBot)
	mux.HandleFunc("POST /api/games/{id}/seats/{idx}/invite", s.inviteSeat)
	mux.HandleFunc("DELETE /api/games/{id}/seats/{idx}/invite", s.cancelSeatInvite)
	mux.HandleFunc("POST /api/games/{id}/seats/{idx}/invite/decline", s.declineInvite)
	mux.HandleFunc("POST /api/games/{id}/seat/resolve", s.resolveSeat)
	mux.HandleFunc("POST /api/games/{id}/leave", s.leaveSeat)
	mux.HandleFunc("POST /api/games/{id}/start", s.startGame)
	mux.HandleFunc("GET /api/games/{id}/replay", s.getReplay)
	mux.HandleFunc("GET /api/games/{id}/events", s.getGameEvents)
	mux.HandleFunc("GET /api/games/{id}/ratings", s.getGameRatings)
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
	// Literal /search must precede the {userId} pattern.
	mux.HandleFunc("GET /api/users/search", s.searchProfiles)
	mux.HandleFunc("GET /api/users/{userId}", s.getPublicProfile)
	mux.HandleFunc("GET /api/leaderboard", s.getLeaderboard)

	inner := loggingMiddleware(s.log, metricsMiddleware(corsMiddleware(s.allowedOrigins, jwtMiddleware(s.verifier, s.log, mux))))

	// otelhttp wraps the entire app handler so every request starts a server
	// span. Skip the health probes — kubelet hits them every 5–20 s and the
	// spans would drown out useful traffic. patternSpanNamer sits between
	// otelhttp and the mux so it sees r.Pattern after dispatch and can
	// rename the span from the generic "http.request" to "{METHOD} {pattern}"
	// — collapses every game id under the same span name in Tempo searches.
	app := otelhttp.NewHandler(patternSpanNamer(inner), "http.request",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
	)

	// /metrics bypasses the CORS/auth/log middleware via a top-level mux.
	top := http.NewServeMux()
	top.Handle("GET /metrics", metricsHandler())
	top.Handle("/", app)
	return top
}

// patternSpanNamer renames the active OTel span from "http.request" to
// "{METHOD} {pattern}" once the mux has matched the route, and stamps the
// matched pattern as an http.route attribute. It runs BEFORE otelhttp ends
// the span (so the rename takes effect) but AFTER the mux dispatch (so
// r.Pattern is populated). Without this, Tempo sees every concrete URL —
// including all distinct game ids — as a separate span name.
func patternSpanNamer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Pattern == "" {
			return
		}
		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			return
		}
		span.SetName(r.Method + " " + r.Pattern)
		span.SetAttributes(attribute.String("http.route", r.Pattern))
	})
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
	s.store.StartMatcher(ctx, s.log, s.fanMatched, s.fanQueueUpdate)
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

// createGame creates a private game and auto-joins the caller. Authenticated
// users get their name from the profile; anonymous callers must supply one.
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
	// Public games are created implicitly by matchmaking, not here.
	if vis != VisibilityPrivate {
		writeError(w, http.StatusBadRequest, "only private games are creatable directly; use /matchmake for public")
		return
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
		writeError(w, http.StatusBadRequest, "name required for anonymous create")
		return
	}

	rec, err := s.store.Create(r.Context(), req.Players, vis)
	if err != nil {
		s.log.Error("create game", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create game")
		return
	}
	gamesCreatedTotal.WithLabelValues(strconv.Itoa(req.Players), string(vis)).Inc()
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

// matchmakeGame — TEST FIXTURE ONLY. Production matchmaking is the async queue
// (POST /api/matchmake/enqueue + /ws/lobby); this synchronous endpoint just
// lets server tests seat two users without the queue + matcher + Postgres.
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

// displayNameFor resolves a display name by priority: profiles.display_name,
// then the JWT's user_metadata.display_name, then the email local-part, then
// "Joueur". Used by every auto-join path so users never retype a known name.
func (s *Server) displayNameFor(ctx context.Context, u *AuthUser) string {
	if p, err := s.store.Profile(ctx, u.ID); err == nil && p != nil && p.DisplayName != "" {
		return p.DisplayName
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if at := strings.IndexByte(u.Email, '@'); at > 0 {
		return u.Email[:at]
	}
	if u.Email != "" {
		return u.Email
	}
	return "Joueur"
}

// addBot fills an empty seat with a bot (private waiting games only).
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

// removeBot vacates a bot seat in a private waiting game. No token check: the
// Store guards private+waiting+seat-is-bot, so it can't kick a human.
func (s *Server) removeBot(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	seatIdx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid seat index")
		return
	}
	rec, err := s.store.RemoveBot(r.Context(), gameID, seatIdx)
	if err != nil {
		writeError(w, statusForRemoveBotError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

// inviteSeatRequest is the body of the seat-invite endpoint. userId enforces
// "this seat is for that user" at join time; displayName is the label shown
// until they join.
type inviteSeatRequest struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

// inviteSeat reserves an empty seat for a named user in a private waiting game;
// pickSeatForUser routes them to it when they join. No token check (same
// posture as addBot — the URL is the shared secret).
func (s *Server) inviteSeat(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	seatIdx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid seat index")
		return
	}
	var req inviteSeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.UserID == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "userId and displayName are required")
		return
	}
	rec, err := s.store.InviteSeat(r.Context(), gameID, seatIdx, req.UserID, req.DisplayName)
	if err != nil {
		writeError(w, statusForInviteSeatError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	// Push to the invitee's lobby WS so they get a toast even off this page.
	// FromName is empty for anonymous hosts.
	fromName := ""
	fromUserID := ""
	if u, ok := userFromContext(r.Context()); ok {
		fromUserID = u.ID
		fromName = s.displayNameFor(r.Context(), u)
	}
	s.publishLobby(req.UserID, LobbyEventInviteReceived, LobbyInvitePayload{
		GameID:     gameID,
		SeatIndex:  seatIdx,
		FromName:   fromName,
		FromUserID: fromUserID,
	})
	writeJSON(w, http.StatusOK, dto)
}

// cancelSeatInvite clears a pending invitation, freeing the seat. Guards
// require the seat to actually carry an invitation, so it can't kick humans
// or vacate bots (those have their own endpoints).
func (s *Server) cancelSeatInvite(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	seatIdx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid seat index")
		return
	}
	// Capture the invitee before the store clears it, so invite_cancelled
	// reaches the right inbox.
	inviteeID := ""
	if rec, ok, err := s.store.Get(r.Context(), gameID); err == nil && ok {
		rec.Lock()
		if seatIdx >= 0 && seatIdx < len(rec.Seats) {
			seat := rec.Seats[seatIdx]
			if !seat.Occupied && !seat.IsBot {
				inviteeID = seat.UserID
			}
		}
		rec.Unlock()
	}
	rec, err := s.store.CancelSeatInvite(r.Context(), gameID, seatIdx)
	if err != nil {
		writeError(w, statusForInviteSeatError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	if inviteeID != "" {
		s.publishLobby(inviteeID, LobbyEventInviteCancelled, LobbyInvitePayload{
			GameID:    gameID,
			SeatIndex: seatIdx,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

func statusForInviteSeatError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadSeatIndex):
		return http.StatusBadRequest
	case errors.Is(err, ErrSeatTaken),
		errors.Is(err, ErrSeatNotInvited),
		errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrBotsOnPublic):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// startGame fills empty seats with bots and flips a private game to playing.
// Any seated participant (seat token) may start it.
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
	case errors.Is(err, ErrNotHost):
		return http.StatusForbidden
	case errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrPublicCannotStart),
		errors.Is(err, ErrTooFewToStart):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// leaveSeat frees the caller's seat in a still-waiting game.
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

func statusForRemoveBotError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadSeatIndex):
		return http.StatusBadRequest
	case errors.Is(err, ErrSeatNotBot),
		errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrBotsOnPublic):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// offerRematch adds the caller's seat to the acceptance set (bots pre-accept).
// Once every human seat has accepted, a new pre-seated game is created and its id
// is set on the original's RematchGameID. The broadcast state carries that id;
// clients navigate to it and resolve their seat over HTTP — no token is pushed.
func (s *Server) offerRematch(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := s.store.OfferRematch(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForRematchError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

// declineRematch withdraws or refuses the pending offer — same outcome either
// way: offer cleared, everyone back to "propose".
func (s *Server) declineRematch(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing player token")
		return
	}
	rec, err := s.store.DeclineRematch(r.Context(), gameID, token)
	if err != nil {
		writeError(w, statusForRematchError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

// declineInvite is the invitee refusing an invitation. Auth is the JWT (no seat
// token yet); only the invited userID may call it.
func (s *Server) declineInvite(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	seatIdx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid seat index")
		return
	}
	u, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required to decline an invitation")
		return
	}
	rec, err := s.store.DeclineSeatInvite(r.Context(), gameID, seatIdx, u.ID)
	if err != nil {
		writeError(w, statusForDeclineInviteError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, dto)
}

func statusForDeclineInviteError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadSeatIndex):
		return http.StatusBadRequest
	case errors.Is(err, ErrNotInvitee):
		return http.StatusForbidden
	case errors.Is(err, ErrSeatNotInvited),
		errors.Is(err, ErrNotPlaying),
		errors.Is(err, ErrBotsOnPublic):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// resignGame, offerDraw, acceptDraw, declineDraw share one shape: extract the
// seat token, call a Store method, broadcast new state on success.

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

// endByConcession is the shared body of resign + accept-draw.
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
	// WinKind.String() is a low-cardinality metric label.
	gamesFinishedTotal.WithLabelValues(dto.WinKind.String()).Inc()
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
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNotFinished),
		errors.Is(err, ErrNoRematchOffer):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// catchupLimit caps how many events one /events call returns, so a long-
// disconnected (or hostile) client can't pull unbounded rows. ~200-400 covers
// a full 6P game.
const catchupLimit = 1000

// getGameEvents is the reconnect catch-up endpoint: returns events with
// seq > since, ascending. No extra auth — the game ID already exposes /get.
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

// getGameRatings serves the Elo snapshot. Returns Rated=false for
// non-matchmaking games so the client hides the Elo section. No auth — public
// per game ID.
func (s *Server) getGameRatings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	gr, err := s.store.RatingsForGame(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found")
			return
		}
		s.log.Error("ratings for game", "game", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load ratings")
		return
	}
	writeJSON(w, http.StatusOK, gr)
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

// resolveSeat hands the authed caller the token for the seat they already own,
// pulled by JWT when their client lands on a pre-seated game without local creds.
func (s *Server) resolveSeat(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	gameID := r.PathValue("id")
	seat, token, err := s.store.ResolveSeat(r.Context(), gameID, u.ID)
	if err != nil {
		writeError(w, statusForResolveSeatError(err), err.Error())
		return
	}
	rec, ok, err := s.store.Get(r.Context(), gameID)
	if err != nil || !ok {
		writeError(w, http.StatusInternalServerError, "could not reload game")
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	// Broadcast so other pods invalidate their cache and pick up the rotated
	// token hash (mirrors joinGame); the DTO payload itself is unchanged.
	s.events.Publish(gameID, eventState(dto))
	writeJSON(w, http.StatusOK, joinResponse{Game: dto, Seat: toSeatDTO(seat), Token: token})
}

func statusForResolveSeatError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrSeatNotForUser):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
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
	movesPlayedTotal.WithLabelValues("human").Inc()

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
	case errors.Is(err, ErrSeatReserved):
		return http.StatusForbidden
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
