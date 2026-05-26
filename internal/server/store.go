package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/alexis/gemline/internal/ai"
	"github.com/alexis/gemline/internal/elo"
	"github.com/alexis/gemline/internal/game"
)

type Status string

const (
	StatusWaiting  Status = "waiting"
	StatusPlaying  Status = "playing"
	StatusFinished Status = "finished"
)

// Visibility decides whether a game shows up in the public lobby. Private
// games are still joinable by anyone holding the URL — visibility is purely
// about *discovery*, not access control.
type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

var (
	ErrGameNotFound  = errors.New("game not found")
	ErrSeatTaken     = errors.New("seat already taken")
	ErrNoFreeSeat    = errors.New("no free seat")
	ErrBadToken      = errors.New("invalid player token")
	ErrNotPlaying    = errors.New("game is not in playing state")
	ErrNotFinished   = errors.New("game is not finished")
	ErrBadVisibility = errors.New("invalid visibility")
	ErrDrawNotOffered = errors.New("no draw offer pending")
	ErrCannotAcceptOwnDrawOffer = errors.New("the offering player cannot accept their own draw offer")
	ErrDrawAlreadyOffered = errors.New("a draw is already being offered")
	ErrDrawUnsupported  = errors.New("draw is only supported in 2-player games")
	ErrBotsOnPublic      = errors.New("bots cannot be added to public games")
	ErrSeatNotBot        = errors.New("seat is not occupied by a bot")
	ErrSeatReserved      = errors.New("seat is reserved for another player")
	ErrSeatNotInvited    = errors.New("seat has no pending invitation")
	ErrAnonymousOnPublic = errors.New("public games require authentication to join")
	ErrBadSeatIndex      = errors.New("seat index out of range")
	ErrPublicCannotStart = errors.New("public games start automatically; manual start is only for private games")
	ErrTooFewToStart     = errors.New("at least 2 seats must be occupied to start")
	ErrNoRematchOffer    = errors.New("no rematch offer pending")
	ErrNotInvitee        = errors.New("only the invited user can act on this invitation")
	ErrNotHost           = errors.New("only the host can start this game")
)

// Seat is a play slot in a game. Once claimed, only the SHA-256 of the
// player token lives in TokenHash — the plaintext token is returned exactly
// once, when the seat is claimed, and is never persisted. UserID is set
// when an authenticated user claimed the seat; for guests it stays empty.
type Seat struct {
	Index     int        // 0..N-1, also turn order
	Color     game.Color // C1..C6
	Name      string
	TokenHash []byte
	UserID    string // Supabase user UUID, or empty for a guest seat
	Occupied  bool
	IsBot     bool
}

type GameRecord struct {
	mu            sync.Mutex
	ID            string
	State         *game.GameState
	Seats         []Seat
	Status        Status
	Visibility    Visibility
	RematchGameID string // ID of the rematch game spawned from this one, if any
	CreatedAt     time.Time

	// DrawOfferBy is the seat index of the player currently offering a draw,
	// or -1 if no offer is pending. Draw is only supported in 2-player games
	// (see Store.OfferDraw). Lives on GameRecord rather than GameState because
	// it is not part of the engine's pure rule logic and is not persisted —
	// an offer cancels naturally on server restart (the player can re-offer).
	DrawOfferBy int

	// RematchOffer tracks per-seat acceptances of a proposed rematch on a
	// finished game. nil means no offer pending. Not persisted (same posture
	// as DrawOfferBy): on server restart the proposer simply re-clicks.
	RematchOffer *RematchOffer
}

// RematchOffer captures the per-seat acceptance state of a rematch proposal
// on a finished game. The rematch new game is only created once every needed
// human seat has accepted (unanimous). Bots are pre-marked accepted at offer
// creation time; seats that finished the game with Occupied=false are skipped.
//
// Persisted as games.rematch_offer JSONB so it survives cache invalidation
// across pods — without that, a 2-pod deploy would see the second player's
// acceptance land on a fresh load of the game from DB (no offer in memory)
// and create a new offer with only that player accepted, instead of
// completing the existing one.
type RematchOffer struct {
	// AcceptedSeats maps seat index (in the *finished* game) to true.
	// Bots are pre-marked at offer creation; humans are added as they
	// accept. A seat being absent means "still pending".
	AcceptedSeats map[int]bool `json:"acceptedSeats"`
	CreatedAt     time.Time    `json:"createdAt"`
}

func (r *GameRecord) Lock()   { r.mu.Lock() }
func (r *GameRecord) Unlock() { r.mu.Unlock() }

// SeatByToken returns the seat whose token matches `tok` (constant-time
// comparison on the hash to avoid leaking via timing). Caller must hold
// the record lock.
func (r *GameRecord) SeatByToken(tok string) (*Seat, bool) {
	want := hashToken(tok)
	for i := range r.Seats {
		s := &r.Seats[i]
		if len(s.TokenHash) == 0 {
			continue
		}
		if subtle.ConstantTimeCompare(s.TokenHash, want) == 1 {
			return s, true
		}
	}
	return nil, false
}

func (r *GameRecord) AllSeated() bool {
	for _, s := range r.Seats {
		if !s.Occupied {
			return false
		}
	}
	return true
}

// Store is the in-memory cache of games. Persistence is delegated to repo;
// the cache survives only as long as the process, but the DB is the source
// of truth for state that has to outlive a restart. The Store also owns the
// chess clock — it schedules per-game timeout timers and applies the
// resulting forfeits when they fire.
type Store struct {
	mu       sync.Mutex
	games    map[string]*GameRecord
	repo     Repository
	clocks   *clockManager
	presence *presenceManager
	seatRefs map[string]map[int]int // gameID → seatIndex → live connections

	onState     func(gameID string)                             // state changed (forfeit, etc.)
	onPresence  func(gameID string, seatIndex int, online bool) // presence transition
	onDrawOffer func(gameID string, offeredBy int)              // draw offer set (>=0) or cleared (-1)
	onMove      func(gameID string, mv game.MoveResult)         // a move was applied (used to feed WS)
	onRated     func(gameID string)                             // ratings successfully applied (used to push a "rated" WS event)

	// botEngine is shared across all bot seats; it carries its own PRNG so
	// concurrent BestMove calls don't fight over a global random source.
	botEngine *ai.Engine
	// botDelay is the artificial pause before a bot plays, so games don't
	// feel uncannily fast. Tests set it to 0 via WithBotDelay for speed.
	botDelay time.Duration

	// disconnectGrace is the per-Store override for how long a seat may
	// remain disconnected before forfeiting. Defaults to the package-level
	// DisconnectGracePeriod (60s) — tests override it with WithDisconnectGrace
	// so the grace-timeout path can be exercised without sleeping a minute.
	disconnectGrace time.Duration

	// cleanerStop signals the stale-game cleaner goroutine to halt.
	// nil while the cleaner hasn't been started.
	cleanerStop chan struct{}
}

// Repo returns the backing repository. Exposed so wiring code that
// builds the EventPublisher (which needs to AppendEvent) doesn't need
// to be passed the repo separately.
func (s *Store) Repo() Repository { return s.repo }

// Invalidate drops the in-memory cache entry for gameID. The next call
// to Get reloads from the canonical store, picking up any mutation
// committed by another pod. Wired into the backplane's event listener:
// every notification from a different pod's EventPublisher triggers
// Invalidate on the receiving pod, so our cached rec never drifts
// further than one NOTIFY hop behind reality.
//
// Idempotent and cheap; safe to call when nothing is cached. Also
// safe to call concurrently with any other Store method — we only
// touch the games map under s.mu, and live references held by other
// goroutines (e.g. a WS handler reading the rec) keep working off
// their existing pointer. They will, however, see staler-than-usual
// data until they call Get again — which is the contract.
func (s *Store) Invalidate(gameID string) {
	s.mu.Lock()
	delete(s.games, gameID)
	s.mu.Unlock()
}

func NewStore(repo Repository) *Store {
	if repo == nil {
		repo = noopRepo{}
	}
	return &Store{
		games:     make(map[string]*GameRecord),
		repo:      repo,
		clocks:    newClockManager(),
		presence:  newPresenceManager(),
		seatRefs:  make(map[string]map[int]int),
		botEngine:       ai.NewEngine(time.Now().UnixNano()),
		botDelay:        600 * time.Millisecond,
		disconnectGrace: DisconnectGracePeriod,
	}
}

// WithDisconnectGrace overrides the disconnect-grace timeout. Returns the
// receiver for chaining and is intended for tests that need to exercise the
// presence-timeout forfeit path without waiting the production grace
// (60 s).
func (s *Store) WithDisconnectGrace(d time.Duration) *Store {
	s.disconnectGrace = d
	return s
}

// noteSwallowedErr records and logs a persistence error that the store
// opted to swallow (in-memory state is treated as the truth). Replaces
// the previous `_ = err` pattern so an oncall has both a Prom counter
// (gemline_persist_errors_total{op=...}) and a structured log to look
// at. The `op` label stays low-cardinality on purpose — alerts can
// then fire on a sudden non-zero rate without exploding cardinality.
func noteSwallowedErr(op string, err error) {
	if err == nil {
		return
	}
	persistErrorsTotal.WithLabelValues(op).Inc()
	slog.Default().Warn("store: persistence error", "op", op, "err", err)
}

// WithBotDelay overrides the artificial think-time between a bot's turn
// coming up and its move being applied. Tests pass 0 for speed.
func (s *Store) WithBotDelay(d time.Duration) *Store {
	s.botDelay = d
	return s
}

// SetMoveListener registers a callback fired after a move (human or bot) is
// applied. The Server uses it to push the resulting move event over the
// WebSocket for bot-applied moves — human moves go through the HTTP handler
// which already broadcasts.
func (s *Store) SetMoveListener(fn func(gameID string, mv game.MoveResult)) {
	s.onMove = fn
}

// SetStateListener registers a callback fired whenever the Store mutates a
// game outside the request-driven path (clock flag, disconnect forfeit).
// The Server hooks it to a hub broadcast so clients see the new state.
func (s *Store) SetStateListener(fn func(gameID string)) { s.onState = fn }

// SetPresenceListener registers a callback fired whenever a seat's online
// state flips. The Server forwards it as a `presence` WS event so other
// players see the indicator update without a full state push.
func (s *Store) SetPresenceListener(fn func(gameID string, seatIndex int, online bool)) {
	s.onPresence = fn
}

// SetDrawOfferListener registers a callback fired whenever the pending draw
// offer state changes (a player opens or closes one). offeredBy is the
// offering seat index, or -1 when the offer is cleared (declined, withdrawn,
// or auto-cancelled by a move).
func (s *Store) SetDrawOfferListener(fn func(gameID string, offeredBy int)) {
	s.onDrawOffer = fn
}

// SetRatedListener registers a callback fired once per game after the
// Elo update has been persisted (ApplyRatedGame returned true). The
// Server hooks it to a "rated" WS event so the end-of-game modal can
// swap from "calcul du rating…" to the resolved deltas. Called from
// the goroutine spawned in maybeApplyRating; the callback should not
// block.
func (s *Store) SetRatedListener(fn func(gameID string)) {
	s.onRated = fn
}

// Close stops every running clock + presence timer + the stale-game
// cleaner. Call on shutdown.
func (s *Store) Close() {
	s.clocks.CancelAll()
	s.presence.CancelAll()
	if s.cleanerStop != nil {
		close(s.cleanerStop)
		s.cleanerStop = nil
	}
}

// StaleWaitingTTL is the age beyond which a waiting game is considered
// abandoned and gets deleted by the cleaner. Long enough that "we'll
// resume tomorrow" private games survive, short enough that orphaned
// rooms don't accumulate forever.
const StaleWaitingTTL = 7 * 24 * time.Hour

// staleCleanerInterval governs how often the cleaner runs. Once per
// hour is plenty: at our scale the table grows by handful of waiting
// rows per day in the worst case.
const staleCleanerInterval = 1 * time.Hour

// StartStaleGameCleaner spins up a background goroutine that deletes
// waiting games older than StaleWaitingTTL on a regular tick. Safe to
// call on every pod — DELETE is idempotent and the same rows would
// just go to whichever pod ticks first. Idempotent against double
// invocation in a single process.
func (s *Store) StartStaleGameCleaner(log *slog.Logger) {
	if s.cleanerStop != nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	s.cleanerStop = make(chan struct{})
	stop := s.cleanerStop
	go func() {
		ticker := time.NewTicker(staleCleanerInterval)
		defer ticker.Stop()
		// Tick once immediately so a freshly-deployed pod doesn't wait
		// a full hour to clean accumulated junk.
		s.runStaleCleaner(log)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.runStaleCleaner(log)
			}
		}
	}()
}

func (s *Store) runStaleCleaner(log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := s.repo.DeleteStaleWaitingGames(ctx, StaleWaitingTTL)
	if err != nil {
		log.Error("stale game cleaner", "err", err)
		return
	}
	if n > 0 {
		log.Info("stale waiting games cleaned", "count", n, "ttl", StaleWaitingTTL.String())
	}
}

// startInternal is the auth-less common path for transitioning a waiting
// game to playing. Trims unoccupied seats, rebuilds the engine for the
// remaining colours, persists, and schedules the first bot move if
// applicable. Used by Store.Start (private, token-auth) — the public
// multi path doesn't need it any more: matched games are created by the
// matcher directly in `playing` state.
func (s *Store) startInternal(rec *GameRecord) error {
	rec.Lock()
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return ErrNotPlaying
	}
	occupied := make([]Seat, 0, len(rec.Seats))
	for _, st := range rec.Seats {
		if st.Occupied {
			occupied = append(occupied, st)
		}
	}
	if len(occupied) < 2 {
		rec.Unlock()
		return ErrTooFewToStart
	}
	colors := make([]game.Color, len(occupied))
	for i := range occupied {
		occupied[i].Index = i
		colors[i] = occupied[i].Color
	}
	rec.Seats = occupied
	// Win-condition thresholds depend on the actual player count, not the
	// slot count we created with — a 6-seat private room launched with 3
	// players plays under the 3-player rulebook, not the 6-player one. The
	// clock budget is independent of player count and is carried through.
	cfg := game.ConfigFor(len(occupied), rec.State.Config)
	rec.State = game.NewGame(colors, cfg)
	rec.Status = StatusPlaying
	rec.State.StartClock(time.Now())
	s.armClock(rec)
	status := rec.Status
	gameID := rec.ID
	rec.Unlock()

	if err := s.repo.FinalizeStart(context.Background(), gameID, status, cfg); err != nil {
		noteSwallowedErr("start_finalize", err)
	}
	if s.onState != nil {
		s.onState(gameID)
	}
	s.maybeScheduleBot(rec)
	return nil
}

// armClock schedules (or cancels) the timeout for the active player of
// `rec`. Must be called with rec.Lock held.
func (s *Store) armClock(rec *GameRecord) {
	if !rec.State.ClockEnabled() || rec.Status != StatusPlaying || rec.State.IsOver() {
		s.clocks.Cancel(rec.ID)
		return
	}
	remainingMs := rec.State.RemainingForActive(time.Now())
	id := rec.ID
	s.clocks.Schedule(id, time.Duration(remainingMs)*time.Millisecond, func() {
		s.handleFlag(id)
	})
}

// gameEnded is the shared post-finish cleanup: cancel any per-seat
// disconnect-grace timers, drop the in-memory presence refcount entry so
// the seatRefs map doesn't grow unbounded across the server's lifetime, and
// give the clock manager a chance to release its slot. Idempotent — safe to
// call from multiple game-end paths (PlayMove, Resign, Forfeit, Flag, etc.).
func (s *Store) gameEnded(gameID string) {
	s.presence.CancelGame(gameID)
	s.clocks.Cancel(gameID)
	s.mu.Lock()
	delete(s.seatRefs, gameID)
	s.mu.Unlock()
}

// SeatConnected is called by the WS handler after a successful hello. It
// bumps the refcount for that seat and, if the seat just came back online
// (0→1), cancels any pending disconnect-grace timer and notifies listeners.
// `color` is the seat's player color, used to feed into the engine for the
// "is this the active player?" check on the timer side.
func (s *Store) SeatConnected(gameID string, seatIndex int) {
	s.mu.Lock()
	if s.seatRefs[gameID] == nil {
		s.seatRefs[gameID] = make(map[int]int)
	}
	prev := s.seatRefs[gameID][seatIndex]
	s.seatRefs[gameID][seatIndex] = prev + 1
	s.mu.Unlock()

	if prev == 0 {
		s.presence.Cancel(gameID, seatIndex)
		if s.onPresence != nil {
			s.onPresence(gameID, seatIndex, true)
		}
	}
}

// SeatDisconnected is the symmetric call when a WS that owned a seat closes.
// On the 1→0 transition we start a disconnect-grace timer; if it fires
// before the seat comes back, the player forfeits.
func (s *Store) SeatDisconnected(gameID string, seatIndex int) {
	s.mu.Lock()
	if s.seatRefs[gameID] == nil {
		s.mu.Unlock()
		return // unknown game (shouldn't happen, but tolerate)
	}
	prev := s.seatRefs[gameID][seatIndex]
	if prev <= 0 {
		s.mu.Unlock()
		return
	}
	s.seatRefs[gameID][seatIndex] = prev - 1
	now := s.seatRefs[gameID][seatIndex]
	s.mu.Unlock()

	if now != 0 {
		return
	}
	s.presence.Schedule(gameID, seatIndex, s.disconnectGrace, func() {
		s.handleDisconnectTimeout(gameID, seatIndex)
	})
	if s.onPresence != nil {
		s.onPresence(gameID, seatIndex, false)
	}
}

// SeatOnline reports whether the given seat currently has at least one live
// WebSocket connection in this Store.
func (s *Store) SeatOnline(gameID string, seatIndex int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.seatRefs[gameID]; ok {
		return m[seatIndex] > 0
	}
	return false
}

// handleDisconnectTimeout is called by presenceManager when the grace period
// expires for a seat with no live connection.
func (s *Store) handleDisconnectTimeout(gameID string, seatIndex int) {
	ctx := context.Background()
	s.mu.Lock()
	rec, ok := s.games[gameID]
	s.mu.Unlock()
	if !ok {
		return
	}

	rec.Lock()
	if rec.State.IsOver() || rec.Status != StatusPlaying {
		rec.Unlock()
		return
	}
	if seatIndex < 0 || seatIndex >= len(rec.Seats) {
		rec.Unlock()
		return
	}
	loser := rec.Seats[seatIndex].Color
	rec.State.Forfeit(loser)
	rec.Status = StatusFinished
	winner := rec.State.Winner
	winKind := rec.State.WinKind
	rec.Unlock()

	s.gameEnded(gameID)
	_ = s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind)
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
}

// handleFlag is fired by the clock manager when the active player's time
// runs out. It locks the game, applies a forfeit, persists, and notifies
// the broadcast listener if registered.
func (s *Store) handleFlag(gameID string) {
	ctx := context.Background()
	s.mu.Lock()
	rec, ok := s.games[gameID]
	s.mu.Unlock()
	if !ok {
		return // game was evicted; nothing to do
	}

	rec.Lock()
	if rec.State.IsOver() || rec.Status != StatusPlaying {
		rec.Unlock()
		return
	}
	// Recheck the remaining time in case a move landed between the timer
	// firing and us acquiring the lock — if the player just made a move,
	// we shouldn't forfeit them.
	if rec.State.RemainingForActive(time.Now()) > 0 {
		rec.Unlock()
		return
	}
	loser := rec.State.CurrentPlayer().Color
	rec.State.Forfeit(loser)
	rec.Status = StatusFinished
	winner := rec.State.Winner
	winKind := rec.State.WinKind
	rec.Unlock()

	s.gameEnded(gameID)
	if err := s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind); err != nil {
		// Persistence failed but the in-memory state is the truth.
		// We do not retry; an admin can inspect the row if needed.
	}
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
}

// Create initializes a game, persists it, and caches it in memory. Visibility
// controls whether the game is discoverable through matchmaking; passing the
// zero value defaults to private. Bots are *not* claimed at create time —
// callers add them per-seat via AddBot once the game exists (private games
// only).
func (s *Store) Create(ctx context.Context, numPlayers int, vis Visibility) (*GameRecord, error) {
	if vis == "" {
		vis = VisibilityPrivate
	}
	if vis != VisibilityPrivate && vis != VisibilityPublic {
		return nil, ErrBadVisibility
	}
	colors := make([]game.Color, numPlayers)
	seats := make([]Seat, numPlayers)
	for i := 0; i < numPlayers; i++ {
		colors[i] = game.Color(i + 1)
		seats[i] = Seat{Index: i, Color: colors[i]}
	}
	rec := &GameRecord{
		ID: newID(),
		// Create-time cfg is a placeholder: the *real* thresholds are
		// committed at Start (cf. startInternal / ConfigFor) once the actual
		// seated count is known. The DTO overrides this with a live preview
		// while the game is still waiting, so callers never see these stale
		// thresholds as authoritative.
		State:       game.NewGame(colors, game.DefaultConfig(numPlayers)),
		Seats:       seats,
		Status:      StatusWaiting,
		Visibility:  vis,
		CreatedAt:   time.Now(),
		DrawOfferBy: -1,
	}
	if err := s.repo.SaveNewGame(ctx, rec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.games[rec.ID] = rec
	s.mu.Unlock()
	return rec, nil
}

// the lobby is open data.
type LobbyEntry struct {
	GameID    string    `json:"gameId"`
	Players   int       `json:"players"`
	Seated    int       `json:"seated"`
	CreatedAt time.Time `json:"createdAt"`
}

// scanLobbyCache walks the in-memory cache and returns every public waiting
// game it knows about, in most-recent-first order. Used by Matchmake when
// the repo is a noop (hermetic tests) — single-process mode means the cache
// is authoritative for "what public games exist right now".
func (s *Store) scanLobbyCache(limit int) []LobbyEntry {
	s.mu.Lock()
	candidates := make([]*GameRecord, 0, len(s.games))
	for _, rec := range s.games {
		candidates = append(candidates, rec)
	}
	s.mu.Unlock()

	out := make([]LobbyEntry, 0)
	for _, rec := range candidates {
		rec.Lock()
		if rec.Status == StatusWaiting && rec.Visibility == VisibilityPublic {
			seated := 0
			for _, st := range rec.Seats {
				if st.Occupied {
					seated++
				}
			}
			out = append(out, LobbyEntry{
				GameID:    rec.ID,
				Players:   len(rec.Seats),
				Seated:    seated,
				CreatedAt: rec.CreatedAt,
			})
		}
		rec.Unlock()
	}
	// Most recent first — same ordering as the repo path.
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// neededRematchSeats returns the set of seat indices that must accept a
// rematch offer before the new game is created. Bots are pre-accepted at
// offer-creation time, so they are not included. Disconnected seats
// (Occupied=false at game finish) are deliberately ignored — otherwise an
// offer would hang on someone who left before the game even ended.
//
// Caller must hold rec's lock.
// hits persistence and is dropped after the push.
func (s *Store) getOrNotFound(ctx context.Context, id string) (*GameRecord, error) {
	rec, ok, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
	}
	return rec, nil
}

// publishDrawOffer persists the draw_offer_by value and fires the
// draw-offer listener so other pods invalidate their cache and the
// local WS broadcasts the new DTO. The order matters: the persist
// MUST happen before the NOTIFY, otherwise an opponent's accept call
// landing on a different pod after the cache invalidation would reload
// a row with the stale value. Surface the persist error so callers can
// return it; the notify itself is fire-and-forget.
func (s *Store) Get(ctx context.Context, id string) (*GameRecord, bool, error) {
	s.mu.Lock()
	rec, ok := s.games[id]
	s.mu.Unlock()
	if ok {
		return rec, true, nil
	}

	loaded, err := s.repo.LoadGame(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if loaded == nil {
		return nil, false, nil
	}

	// Race-safe cache fill: if another goroutine just loaded the same game,
	// keep that copy so callers don't fork the in-memory state.
	s.mu.Lock()
	freshlyCached := false
	if existing, found := s.games[id]; found {
		loaded = existing
	} else {
		s.games[id] = loaded
		freshlyCached = true
	}
	s.mu.Unlock()

	// First time we see this game in memory: arm its clock if it's in play.
	if freshlyCached {
		loaded.Lock()
		s.armClock(loaded)
		loaded.Unlock()
	}
	return loaded, true, nil
}

// Join claims a seat in `gameID` for `name`. If seatIdx is negative, the
// first free seat is chosen. `userID` is the Supabase user UUID for an
// authenticated join, or "" for a guest. Returns the claimed seat and the
// plaintext player token (only available here — only its hash is persisted).
func (s *Store) Join(ctx context.Context, gameID, name, userID string, seatIdx int) (*Seat, string, error) {
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrGameNotFound
	}

	rec.Lock()
	defer rec.Unlock()

	if rec.Status != StatusWaiting {
		return nil, "", ErrNotPlaying
	}
	// Public games are matchmaking-only. Anonymous joiners would never have
	// a stable identity to rate against, so we reject them here rather than
	// silently producing an unrated game from a public slot.
	if rec.Visibility == VisibilityPublic && userID == "" {
		return nil, "", ErrAnonymousOnPublic
	}

	idx := seatIdx
	if idx < 0 {
		// Auto-pick: if any seat is reserved for the caller (private
		// game invitation), claim that one first; otherwise take any
		// seat that isn't held for someone else.
		idx = pickSeatForUser(rec.Seats, userID)
		if idx < 0 {
			return nil, "", ErrNoFreeSeat
		}
	} else {
		if idx >= len(rec.Seats) {
			return nil, "", ErrNoFreeSeat
		}
		if rec.Seats[idx].Occupied {
			return nil, "", ErrSeatTaken
		}
		// If a specific seat is requested, enforce the reservation:
		// authed users can claim their own reserved seat; anyone else
		// (anon, or a different authed user) bounces.
		if reserved := rec.Seats[idx].UserID; reserved != "" && reserved != userID {
			return nil, "", ErrSeatReserved
		}
	}

	token := newToken()
	rec.Seats[idx].Name = name
	rec.Seats[idx].TokenHash = hashToken(token)
	rec.Seats[idx].UserID = userID
	rec.Seats[idx].Occupied = true
	startedPlaying := false
	if rec.AllSeated() {
		rec.Status = StatusPlaying
		startedPlaying = true
		rec.State.StartClock(time.Now())
	}

	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[idx], rec.Status); err != nil {
		return nil, "", err
	}

	if startedPlaying {
		s.armClock(rec)
	}
	// Whether we just started or the human just joined an already-started
	// game (impossible today but cheap to handle), the active player might
	// be a bot — kick them now.
	if startedPlaying {
		go s.maybeScheduleBot(rec)
	}
	return &rec.Seats[idx], token, nil
}

// PlayMove authenticates the bearer token, applies the move to game state,
// persists the move + any win-state change, and returns the engine's result.
func (s *Store) PlayMove(ctx context.Context, gameID, token string, q, r int) (game.MoveResult, *GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return game.MoveResult{}, nil, err
	}

	rec.Lock()
	defer rec.Unlock()

	seat, ok := rec.SeatByToken(token)
	if !ok {
		return game.MoveResult{}, rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		return game.MoveResult{}, rec, ErrNotPlaying
	}

	ordinal := len(rec.State.History)
	move := game.Move{Player: seat.Color, Pos: game.Position{Q: q, R: r}}
	res, err := rec.State.ApplyMove(move, time.Now())
	if err != nil {
		return res, rec, err
	}
	if rec.State.IsOver() {
		rec.Status = StatusFinished
	}
	// A move from either player automatically rescinds any pending draw
	// offer — you can't both ask for a draw and keep playing. The subsequent
	// move broadcast carries the new game DTO (DrawOfferBy=-1) to clients,
	// so we don't fire the draw-offer listener here — doing so under the
	// rec.Lock held by the deferred Unlock would deadlock anyway when the
	// listener tries to re-enter through store.Get.
	hadDrawOffer := rec.DrawOfferBy >= 0
	rec.DrawOfferBy = -1

	if perr := s.repo.AppendMove(ctx, gameID, ordinal, move, rec.State.Winner, rec.State.WinKind, rec.Status); perr != nil {
		// We've already mutated in-memory state; returning the persist error
		// means the client retries and the DB stays out of sync. That's worse
		// than the inconsistency. Surface it but keep the in-memory truth.
		return res, rec, perr
	}
	if hadDrawOffer {
		// Move just rescinded a pending draw offer — clear draw_offer_by in
		// the DB so other pods reload the right value rather than the stale
		// offerer. Skipped when no offer was pending to avoid a write per
		// move on the hot path.
		if perr := s.repo.SaveDrawOffer(ctx, gameID, -1); perr != nil {
			return res, rec, perr
		}
	}

	// Re-arm or cancel the chess clock for the next player. Called while
	// rec is still locked — armClock doesn't take the rec lock itself.
	s.armClock(rec)
	// If the next active player is a bot and the game is still in play,
	// schedule its move. We can't trigger it synchronously here: the bot's
	// PlayMove takes rec.Lock(), which is held by the deferred Unlock above.
	defer s.maybeScheduleBot(rec)
	// If this move ended the game, run the same post-finish cleanup as the
	// resign/forfeit/flag paths (release presence timers + the seatRefs
	// entry) and kick off the rating update. Both go through goroutines
	// rather than defers because the defer LIFO order would run them
	// BEFORE the rec.Unlock above, and gameEnded/maybeApplyRating reach
	// for s.mu / rec.Lock respectively.
	if rec.State.IsOver() {
		gid := gameID
		go func() {
			s.gameEnded(gid)
			s.maybeApplyRating(rec)
		}()
	}
	return res, rec, nil
}

// AddBot claims an empty seat with a bot. Restricted to private games —
// matchmade public games must be filled by humans through matchmaking, not
// stuffed by whoever holds the URL. When the placement fills the last seat
// the game transitions to playing and (if seat 0 is a bot) its first move
// is scheduled.
// Start finalises a private game in `waiting`: unoccupied seats are dropped
// (they stay empty — no bot fillers, the host decides upfront whether to
// add bots before clicking Start), the engine is rebuilt with the remaining
// players' colours, status flips to `playing`, and the clock starts. The
// caller's seat token authorises the start; the trust model is "you have
// the private URL = you can decide to start".
//
// Disallowed:
//   - Public games (matchmaking owns those — they auto-start once the
//     opponent arrives).
//   - Games with fewer than 2 occupied seats (no opponent to play against).
func (s *Store) Start(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	// Auth & visibility checks live here (the HTTP-facing path); the
	// trimming + persistence logic is shared with the auto-promoter
	// in startInternal.
	rec.Lock()
	if rec.Visibility != VisibilityPrivate {
		rec.Unlock()
		return rec, ErrPublicCannotStart
	}
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	// Host-only start: only seat 0 (the creator, set when POST /api/games
	// auto-joined them) may kick off the game. Stops a guest from racing
	// the host on Start before the host has finished filling the lobby
	// (adding bots, inviting a friend, etc.). If the host has left their
	// seat the game can't be started until they come back — that's the
	// intended behaviour, not an oversight.
	if seat.Index != 0 {
		rec.Unlock()
		return rec, ErrNotHost
	}
	rec.Unlock()
	if err := s.startInternal(rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *Store) AddBot(ctx context.Context, gameID string, seatIdx int) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.Visibility != VisibilityPrivate {
		rec.Unlock()
		return rec, ErrBotsOnPublic
	}
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return rec, ErrBadSeatIndex
	}
	if rec.Seats[seatIdx].Occupied {
		rec.Unlock()
		return rec, ErrSeatTaken
	}

	token := newToken()
	rec.Seats[seatIdx].Name = botName(seatIdx)
	rec.Seats[seatIdx].TokenHash = hashToken(token)
	rec.Seats[seatIdx].Occupied = true
	rec.Seats[seatIdx].IsBot = true

	startedPlaying := false
	if rec.AllSeated() {
		rec.Status = StatusPlaying
		startedPlaying = true
		rec.State.StartClock(time.Now())
	}

	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[seatIdx], rec.Status); err != nil {
		rec.Unlock()
		return rec, err
	}
	if startedPlaying {
		s.armClock(rec)
	}
	rec.Unlock()

	if startedPlaying {
		// Active player may itself be a bot — kick its turn.
		s.maybeScheduleBot(rec)
	}
	return rec, nil
}

// RemoveBot vacates a bot-occupied seat in a private waiting game. The
// inverse of AddBot: same guards (private + waiting + seat in range)
// plus the seat must actually be occupied by a bot (vs. a human, who
// would leave via Store.LeaveSeat with their own token). Resets the
// seat to its empty state and persists.
func (s *Store) RemoveBot(ctx context.Context, gameID string, seatIdx int) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.Visibility != VisibilityPrivate {
		rec.Unlock()
		return rec, ErrBotsOnPublic
	}
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return rec, ErrBadSeatIndex
	}
	if !rec.Seats[seatIdx].Occupied || !rec.Seats[seatIdx].IsBot {
		rec.Unlock()
		return rec, ErrSeatNotBot
	}

	rec.Seats[seatIdx].Name = ""
	rec.Seats[seatIdx].TokenHash = nil
	rec.Seats[seatIdx].Occupied = false
	rec.Seats[seatIdx].IsBot = false

	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[seatIdx], rec.Status); err != nil {
		rec.Unlock()
		return rec, err
	}
	rec.Unlock()
	return rec, nil
}

// InviteSeat reserves a seat for a named user in a private waiting
// game. The seat takes the invitee's userID + display name but stays
// Occupied=false until the user actually shows up and joins via the
// game URL — the join logic prefers the user's reserved seat when
// they arrive, and refuses to let other players claim it.
//
// Same auth posture as AddBot: no caller credentials needed. The
// game URL is the shared secret for a private game; anyone who has
// it can rearrange seats. The Store guards on visibility=private +
// status=waiting + seat empty + valid userID/name so the endpoint
// can't be abused for state mutation outside that surface.
func (s *Store) InviteSeat(ctx context.Context, gameID string, seatIdx int, inviteeID, inviteeName string) (*GameRecord, error) {
	if inviteeID == "" || inviteeName == "" {
		return nil, ErrBadSeatIndex // reuse 400 family — body validation
	}
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.Visibility != VisibilityPrivate {
		rec.Unlock()
		return rec, ErrBotsOnPublic
	}
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return rec, ErrBadSeatIndex
	}
	if rec.Seats[seatIdx].Occupied {
		rec.Unlock()
		return rec, ErrSeatTaken
	}

	// Same shape as a bot reservation, minus IsBot. occupied stays
	// false so AllSeated() doesn't flip the game to playing on an
	// invite. The seat is "reserved" — token gets minted at actual
	// join time.
	rec.Seats[seatIdx].Name = inviteeName
	rec.Seats[seatIdx].UserID = inviteeID
	rec.Seats[seatIdx].TokenHash = nil
	rec.Seats[seatIdx].Occupied = false
	rec.Seats[seatIdx].IsBot = false

	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[seatIdx], rec.Status); err != nil {
		rec.Unlock()
		return rec, err
	}
	rec.Unlock()
	return rec, nil
}

// CancelSeatInvite clears a pending invitation, returning the seat
// to its empty state. Guards on visibility + waiting + the seat
// actually carrying an invitation (UserID set, not Occupied,
// !IsBot — ErrSeatNotInvited otherwise so the endpoint can't be
// used to kick humans or bots).
func (s *Store) CancelSeatInvite(ctx context.Context, gameID string, seatIdx int) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.Visibility != VisibilityPrivate {
		rec.Unlock()
		return rec, ErrBotsOnPublic
	}
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return rec, ErrBadSeatIndex
	}
	st := &rec.Seats[seatIdx]
	if st.Occupied || st.IsBot || st.UserID == "" {
		rec.Unlock()
		return rec, ErrSeatNotInvited
	}

	st.Name = ""
	st.UserID = ""
	st.TokenHash = nil
	st.Occupied = false
	st.IsBot = false

	if err := s.repo.UpdateSeat(ctx, gameID, st, rec.Status); err != nil {
		rec.Unlock()
		return rec, err
	}
	rec.Unlock()
	return rec, nil
}

// DeclineSeatInvite is the invitee-side counterpart to CancelSeatInvite:
// the invited user themselves refuses the seat. Same end-state as cancel
// (seat returns to empty), but auth differs — only the invited userID may
// call this. Used by the "Refuser" button on the invitee's game page.
func (s *Store) DeclineSeatInvite(ctx context.Context, gameID string, seatIdx int, callerUserID string) (*GameRecord, error) {
	if callerUserID == "" {
		return nil, ErrNotInvitee
	}
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.Visibility != VisibilityPrivate {
		rec.Unlock()
		return rec, ErrBotsOnPublic
	}
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return rec, ErrBadSeatIndex
	}
	st := &rec.Seats[seatIdx]
	if st.Occupied || st.IsBot || st.UserID == "" {
		rec.Unlock()
		return rec, ErrSeatNotInvited
	}
	if st.UserID != callerUserID {
		rec.Unlock()
		return rec, ErrNotInvitee
	}

	st.Name = ""
	st.UserID = ""
	st.TokenHash = nil
	st.Occupied = false
	st.IsBot = false

	if err := s.repo.UpdateSeat(ctx, gameID, st, rec.Status); err != nil {
		rec.Unlock()
		return rec, err
	}
	rec.Unlock()
	return rec, nil
}

// Matchmake — TEST FIXTURE ONLY. Production matchmaking goes through the
// async queue + matcher tick path (POST /api/matchmake/enqueue +
// /ws/lobby match_found). This synchronous "find-or-create a public
// waiting game" routine is preserved as a convenience for the server
// tests that need "two authenticated users seated in a public game"
// without spinning up a real Postgres queue. The companion HTTP
// endpoint POST /api/games/matchmake exists for the same reason.
//
// Behaviour: returns a public waiting game of the requested player
// count with at least one free seat that `excludeUserID` is *not*
// already seated in. For 2-player (1v1) games we score candidates by
// Elo proximity (age-widened band). For 3+ player (multi) we score by
// "most filled, oldest first". If no candidate matches, a fresh public
// game is created.
func (s *Store) Matchmake(ctx context.Context, players int, excludeUserID string) (*GameRecord, error) {
	candidates, err := s.repo.LobbyGames(ctx, 50)
	if err != nil {
		return nil, err
	}
	if candidates == nil {
		candidates = s.scanLobbyCache(50)
	}
	// Matchmaking always pairs within a single rating bucket; players=2
	// uses the 1v1 ratings, players>2 uses the multi ratings.
	ratingMode := RatingModeMulti
	if players == 2 {
		ratingMode = RatingMode1v1
	}
	callerRating := s.fetchCallerRating(ctx, excludeUserID, ratingMode)

	type scored struct {
		rec   *GameRecord
		delta float64
		age   time.Duration
		seated int
	}
	var picks []scored
	now := time.Now()
	for _, c := range candidates {
		if c.Players != players || c.Seated >= c.Players {
			continue
		}
		rec, ok, err := s.Get(ctx, c.GameID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rec.Lock()
		joinable := rec.Status == StatusWaiting && !rec.AllSeated()
		if joinable && excludeUserID != "" {
			for _, seat := range rec.Seats {
				if seat.UserID == excludeUserID {
					joinable = false
					break
				}
			}
		}
		var occupantUserIDs []string
		seated := 0
		if joinable {
			for _, seat := range rec.Seats {
				if seat.Occupied {
					seated++
					if seat.UserID != "" {
						occupantUserIDs = append(occupantUserIDs, seat.UserID)
					}
				}
			}
		}
		rec.Unlock()
		if !joinable {
			continue
		}

		age := now.Sub(c.CreatedAt)
		avg := s.averageRating(ctx, occupantUserIDs, ratingMode)
		delta := math.Abs(float64(callerRating) - float64(avg))

		if players == 2 && !withinBand(callerRating, avg, age) {
			// Outside this candidate's tolerance — skip for now. The
			// caller will create their own room (or land on a more
			// permissive existing one).
			continue
		}
		picks = append(picks, scored{rec: rec, delta: delta, age: age, seated: seated})
	}
	if len(picks) > 0 {
		var best scored
		bestIdx := -1
		for i, p := range picks {
			if bestIdx == -1 {
				best, bestIdx = p, i
				continue
			}
			// 1v1: smallest rating delta wins. Multi: most-filled, with
			// age as the tiebreaker so older rooms drain first.
			better := false
			if players == 2 {
				better = p.delta < best.delta
			} else {
				if p.seated != best.seated {
					better = p.seated > best.seated
				} else {
					better = p.age > best.age
				}
			}
			if better {
				best, bestIdx = p, i
			}
		}
		_ = bestIdx
		return best.rec, nil
	}
	return s.Create(ctx, players, VisibilityPublic)
}

// fetchCallerRating resolves a user's current Elo in the given mode,
// falling back to the default when they have no rated games yet (or no
// user, for anonymous — not reachable for matchmake but cheap to guard).
func (s *Store) fetchCallerRating(ctx context.Context, userID, mode string) int {
	if userID == "" {
		return elo.DefaultRating
	}
	r, err := s.repo.RatingFor(ctx, userID, mode)
	if err != nil || r.Games == 0 {
		return elo.DefaultRating
	}
	return r.Rating
}

// averageRating returns the mean Elo of the supplied users in the given
// mode, with unrated users taking the default. Empty list → default rating.
func (s *Store) averageRating(ctx context.Context, userIDs []string, mode string) int {
	if len(userIDs) == 0 {
		return elo.DefaultRating
	}
	ratings, err := s.repo.RatingsFor(ctx, userIDs, mode)
	if err != nil {
		return elo.DefaultRating
	}
	sum := 0
	for _, r := range ratings {
		if r.Games == 0 {
			sum += elo.DefaultRating
		} else {
			sum += r.Rating
		}
	}
	return sum / len(ratings)
}

// LeaveSeat frees the seat behind `token` and broadcasts the new state.
// Only allowed while the game is still in `waiting`: leaving a game in play
// is what Resign is for. The seat's name/user/bot/token are all cleared so a
// fresh join can reuse the slot.
func (s *Store) LeaveSeat(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}
	rec.Lock()
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusWaiting {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	seatIdx := seat.Index
	rec.Seats[seatIdx] = Seat{
		Index: seatIdx,
		Color: seat.Color,
	}
	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[seatIdx], rec.Status); err != nil {
		rec.Unlock()
		return rec, err
	}
	rec.Unlock()
	return rec, nil
}

// Resign ends a game in progress by recording that the seat behind `token`
// gave up. The win-kind is WinResign; the survivor (in 2-player mode) is the
// winner. Persists the new outcome and notifies state listeners so the WS
// hub broadcasts the final snapshot.
func (s *Store) Resign(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	rec.State.Resign(seat.Color)
	rec.Status = StatusFinished
	winner := rec.State.Winner
	winKind := rec.State.WinKind
	// Any pending draw offer is cleared along with the game ending.
	hadOffer := rec.DrawOfferBy >= 0
	rec.DrawOfferBy = -1
	rec.Unlock()

	s.gameEnded(gameID)
	if err := s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind); err != nil {
		// In-memory truth wins (same policy as handleFlag) — log + bump
		// the persist-errors counter and keep going.
		noteSwallowedErr("resign_outcome_persist", err)
	}
	if hadOffer {
		// Best-effort: surface the clear to other pods. Persist failure
		// here doesn't roll the engine's resign back — in-memory truth wins.
		if err := s.publishDrawOffer(ctx, gameID, -1); err != nil {
			noteSwallowedErr("resign_draw_clear", err)
		}
	}
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
	return rec, nil
}

// OfferDraw records that the seat behind `token` would accept a draw if the
// opponent agrees. Restricted to 2-player games per the design call. The
// offer auto-cancels on any subsequent move (see PlayMove). Re-offering with
// an already-pending offer is rejected so the UI surfaces "already pending"
// cleanly rather than silently overwriting.
func (s *Store) maybeApplyRating(rec *GameRecord) {
	rec.Lock()
	if rec.Status != StatusFinished {
		rec.Unlock()
		return
	}
	if rec.Visibility != VisibilityPublic || len(rec.Seats) < 2 {
		rec.Unlock()
		return
	}
	// Every seat must be an authed human, otherwise we'd rate a mixed
	// human/bot game which would corrupt ratings.
	for _, st := range rec.Seats {
		if st.IsBot || st.UserID == "" {
			rec.Unlock()
			return
		}
	}
	seats := append([]Seat(nil), rec.Seats...)
	winnerColor := rec.State.Winner
	gameID := rec.ID
	rec.Unlock()

	if len(seats) == 2 {
		go s.applyRating1v1(gameID, seats, winnerColor)
	} else if winnerColor != game.Empty {
		go s.applyRatingMulti(gameID, seats, winnerColor)
	}
}

// applyRating1v1 implements the standard pairwise Elo update used since
// the very first ratings commit, scoped now to mode='1v1'. Draw counts
// land on both players (zero net delta, but games/draws columns bumped).
func (s *Store) applyRating1v1(gameID string, seats []Seat, winnerColor game.Color) {
	ctx := context.Background()
	a, b := seats[0], seats[1]
	ratings, err := s.repo.RatingsFor(ctx, []string{a.UserID, b.UserID}, RatingMode1v1)
	if err != nil || len(ratings) != 2 {
		return
	}
	rA := ratingOrDefault(ratings[0])
	rB := ratingOrDefault(ratings[1])

	var outcomeA, outcomeB elo.Outcome
	var resultA, resultB rune
	switch winnerColor {
	case a.Color:
		outcomeA, outcomeB = elo.Win, elo.Loss
		resultA, resultB = 'W', 'L'
	case b.Color:
		outcomeA, outcomeB = elo.Loss, elo.Win
		resultA, resultB = 'L', 'W'
	default:
		outcomeA, outcomeB = elo.Draw, elo.Draw
		resultA, resultB = 'D', 'D'
	}
	updates := []RatingUpdate{
		{UserID: a.UserID, OldRating: rA, NewRating: elo.Update(rA, rB, outcomeA), Result: resultA},
		{UserID: b.UserID, OldRating: rB, NewRating: elo.Update(rB, rA, outcomeB), Result: resultB},
	}
	// EnsureProfile first so the profile row exists before any leaderboard
	// JOIN sees the rating that's about to land. The seat name is what
	// displayNameFor produced at game-create time — already the user's
	// best-known name (profile → email → fallback). Errors here are
	// non-fatal: the rating still applies, the leaderboard just won't
	// show this user until they hit /api/auth/me or set a name.
	if err := s.repo.EnsureProfile(ctx, a.UserID, a.Name); err != nil {
		slog.Default().Error("ensure profile (rated 1v1)", "user", a.UserID, "err", err)
	}
	if err := s.repo.EnsureProfile(ctx, b.UserID, b.Name); err != nil {
		slog.Default().Error("ensure profile (rated 1v1)", "user", b.UserID, "err", err)
	}
	applied, err := s.repo.ApplyRatedGame(ctx, gameID, RatingMode1v1, updates)
	if err != nil {
		slog.Default().Error("apply rated game (1v1)", "game", gameID, "err", err)
		return
	}
	if applied && s.onRated != nil {
		s.onRated(gameID)
	}
}

// applyRatingMulti applies the zero-sum moyenne-des-adversaires extension:
// the winner gains the standard Elo amount measured against the average
// rating of the rest of the field, and that exact gain is split among the
// losers (per-loser loss = winner_gain / N). Multi games that end without
// a winner are skipped at the caller — there's nothing to credit.
func (s *Store) applyRatingMulti(gameID string, seats []Seat, winnerColor game.Color) {
	ctx := context.Background()
	userIDs := make([]string, len(seats))
	for i, st := range seats {
		userIDs[i] = st.UserID
	}
	ratings, err := s.repo.RatingsFor(ctx, userIDs, RatingModeMulti)
	if err != nil || len(ratings) != len(seats) {
		return
	}

	// Separate winner from opponents while preserving the opponents' seat
	// order so RatingUpdate.UserID lines up with the right new rating.
	var (
		winnerSeat   Seat
		winnerRating int
		oppIDs       []string
		oppRatings   []int
	)
	for i, st := range seats {
		r := ratingOrDefault(ratings[i])
		if st.Color == winnerColor {
			winnerSeat = st
			winnerRating = r
		} else {
			oppIDs = append(oppIDs, st.UserID)
			oppRatings = append(oppRatings, r)
		}
	}
	if winnerSeat.UserID == "" {
		return // winner didn't match any seat — shouldn't happen
	}

	results := elo.UpdateMulti(winnerSeat.UserID, winnerRating, oppIDs, oppRatings)
	// Index the old ratings by user_id so we can stuff the right
	// OldRating into each update without re-doing the
	// winner/opponent split.
	oldByID := make(map[string]int, len(seats))
	for i, st := range seats {
		oldByID[st.UserID] = ratingOrDefault(ratings[i])
	}
	updates := make([]RatingUpdate, len(results))
	for i, r := range results {
		updates[i] = RatingUpdate{
			UserID:    r.UserID,
			OldRating: oldByID[r.UserID],
			NewRating: r.NewRating,
			Result:    r.Result,
		}
	}
	// Same belt-and-suspenders as applyRating1v1: every rated player
	// needs a profiles row or the leaderboard INNER JOIN drops them.
	for _, st := range seats {
		if err := s.repo.EnsureProfile(ctx, st.UserID, st.Name); err != nil {
			slog.Default().Error("ensure profile (rated multi)", "user", st.UserID, "err", err)
		}
	}
	applied, err := s.repo.ApplyRatedGame(ctx, gameID, RatingModeMulti, updates)
	if err != nil {
		slog.Default().Error("apply rated game (multi)", "game", gameID, "err", err)
		return
	}
	if applied && s.onRated != nil {
		s.onRated(gameID)
	}
}

func ratingOrDefault(r Rating) int {
	if r.Games == 0 {
		return elo.DefaultRating
	}
	return r.Rating
}

// RatingsForGame is a thin pass-through to the repo. Kept on Store so
// callers don't need a separate reference; consistent with how every
// other read accessor lives here.
func (s *Store) RatingsForGame(ctx context.Context, gameID string) (GameRatings, error) {
	return s.repo.RatingsForGame(ctx, gameID)
}

// Leaderboard surfaces the top-rated players for the given mode, joined
// with their profile name.
func (s *Store) Leaderboard(ctx context.Context, mode string, limit int) ([]LeaderboardEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if mode != RatingMode1v1 && mode != RatingModeMulti {
		mode = RatingMode1v1
	}
	return s.repo.Leaderboard(ctx, mode, limit)
}

func firstFreeSeat(seats []Seat) int {
	for i, s := range seats {
		if !s.Occupied {
			return i
		}
	}
	return -1
}

// pickSeatForUser selects which seat a joining player should claim.
// Priority:
//  1. A seat reserved for this user via an invitation (UserID matches,
//     Occupied is false). Reserved seats are private-game invites set
//     via Store.InviteSeat.
//  2. Any unoccupied seat with no reservation.
//
// Returns -1 if no claimable seat exists (full, or every empty seat is
// reserved for someone else). For anonymous joiners (userID == "")
// step 1 is skipped and only unreserved seats are eligible.
func pickSeatForUser(seats []Seat, userID string) int {
	if userID != "" {
		for i, s := range seats {
			if !s.Occupied && s.UserID == userID {
				return i
			}
		}
	}
	for i, s := range seats {
		if !s.Occupied && s.UserID == "" {
			return i
		}
	}
	return -1
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// hashToken returns the SHA-256 digest of a bearer token. The plaintext is
// never stored; comparison happens on the hash via subtle.ConstantTimeCompare.
func hashToken(tok string) []byte {
	sum := sha256.Sum256([]byte(tok))
	return sum[:]
}

// Profile, UpsertProfile, GamesForUser, StatsForUser are thin wrappers over
// the Repository — they don't go through the in-memory cache because the
// data they return is per-user, not per-game.

func (s *Store) Profile(ctx context.Context, userID string) (*Profile, error) {
	return s.repo.Profile(ctx, userID)
}

func (s *Store) UpsertProfile(ctx context.Context, userID, displayName string) error {
	return s.repo.UpsertProfile(ctx, userID, displayName)
}

// EnsureProfile is the no-overwrite variant. Used at rating-apply time
// (so a user who matchmaked, played, and earned an Elo update always
// has a profile row to JOIN against on the leaderboard) and on the
// auth-me endpoint (lazy first-time profile creation). Never clobbers
// a name the user already chose explicitly.
func (s *Store) EnsureProfile(ctx context.Context, userID, fallbackName string) error {
	if userID == "" || fallbackName == "" {
		return nil
	}
	return s.repo.EnsureProfile(ctx, userID, fallbackName)
}

func (s *Store) GamesForUser(ctx context.Context, userID string, limit int) ([]UserGame, error) {
	return s.repo.GamesForUser(ctx, userID, limit)
}

func (s *Store) StatsForUser(ctx context.Context, userID string) (UserStats, error) {
	return s.repo.StatsForUser(ctx, userID)
}

// PostMessage authenticates the bearer seat token and appends a chat message
// from that seat. The body is trimmed and capped at MaxMessageLength.
func (s *Store) PostMessage(ctx context.Context, gameID, token, body string) (*Message, error) {
	body = trimMessage(body)
	if body == "" {
		return nil, ErrEmptyMessage
	}

	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	defer rec.Unlock()

	seat, ok := rec.SeatByToken(token)
	if !ok {
		return nil, ErrBadToken
	}

	m := &Message{
		GameID:      gameID,
		SeatIndex:   seat.Index,
		AuthorColor: seat.Color,
		AuthorName:  seat.Name,
		Body:        body,
	}
	if err := s.repo.AppendMessage(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) MessagesForGame(ctx context.Context, gameID string, limit int) ([]Message, error) {
	return s.repo.MessagesForGame(ctx, gameID, limit)
}

const MaxMessageLength = 500

var ErrEmptyMessage = errors.New("message body is empty")

func trimMessage(body string) string {
	out := make([]rune, 0, len(body))
	for _, r := range body {
		if r == '\t' {
			r = ' '
		}
		out = append(out, r)
	}
	for len(out) > 0 && (out[0] == ' ' || out[0] == '\n' || out[0] == '\r') {
		out = out[1:]
	}
	for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\n' || out[len(out)-1] == '\r') {
		out = out[:len(out)-1]
	}
	if len(out) > MaxMessageLength {
		out = out[:MaxMessageLength]
	}
	return string(out)
}
