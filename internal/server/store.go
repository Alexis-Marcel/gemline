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
	ErrAnonymousOnPublic = errors.New("public games require authentication to join")
	ErrBadSeatIndex      = errors.New("seat index out of range")
	ErrPublicCannotStart = errors.New("public games start automatically; manual start is only for private games")
	ErrTooFewToStart     = errors.New("at least 2 seats must be occupied to start")
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

	// promoterStop signals the multi-room auto-promotion goroutine to halt.
	// nil while the promoter hasn't been started.
	promoterStop chan struct{}
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

// Close stops every running clock + presence timer + the multi promoter.
// Call on shutdown.
func (s *Store) Close() {
	s.clocks.CancelAll()
	s.presence.CancelAll()
	if s.promoterStop != nil {
		close(s.promoterStop)
		s.promoterStop = nil
	}
}

// StartMultiPromoter spins up the background goroutine that promotes
// public multi-player rooms to `playing` once enough seats are occupied
// and the room has waited long enough. Idempotent — calling twice does
// nothing. Tests skip this entirely (deterministic via Store.Start
// calls instead).
func (s *Store) StartMultiPromoter() {
	if s.promoterStop != nil {
		return
	}
	s.promoterStop = make(chan struct{})
	stop := s.promoterStop
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.checkMultiPromotions()
			}
		}
	}()
}

func (s *Store) checkMultiPromotions() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.games))
	for id := range s.games {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		rec, ok, err := s.Get(context.Background(), id)
		if err != nil || !ok {
			continue
		}
		s.promoteMultiIfReady(rec)
	}
}

// promoteMultiIfReady starts a public multi-player game when the room has
// at least 3 occupied seats and has waited the threshold for its current
// occupancy. Idempotent + safe under concurrent Joins — the actual
// promotion goes through Store.startInternal which re-acquires rec.Lock
// and re-validates state.
func (s *Store) promoteMultiIfReady(rec *GameRecord) {
	rec.Lock()
	if rec.Status != StatusWaiting || rec.Visibility != VisibilityPublic {
		rec.Unlock()
		return
	}
	if len(rec.Seats) < 3 {
		rec.Unlock()
		return
	}
	occupied := 0
	for _, st := range rec.Seats {
		if st.Occupied {
			occupied++
		}
	}
	age := time.Since(rec.CreatedAt)
	rec.Unlock()
	if occupied < 3 {
		return
	}
	if age < multiPromotionThreshold(occupied) {
		return
	}
	_ = s.startInternal(rec)
}

// startInternal is the auth-less common path for transitioning a waiting
// game to playing. Trims unoccupied seats, rebuilds the engine for the
// remaining colours, persists, and schedules the first bot move if
// applicable. Used by both Store.Start (private, token-auth) and the
// auto-promoter (public multi).
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
		_ = err
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

func botName(seatIndex int) string {
	switch seatIndex {
	case 0:
		return "Bot Rouge"
	case 1:
		return "Bot Bleu"
	case 2:
		return "Bot Vert"
	case 3:
		return "Bot Jaune"
	case 4:
		return "Bot Violet"
	default:
		return "Bot"
	}
}

// LobbyEntry is a slimmed-down view of a public waiting game, returned by the
// lobby endpoint. We deliberately don't include seat tokens or chat history —
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

// Rematch creates a fresh game with the same player count, config and
// visibility as `originalID`, and links the two via rematch_game_id. The
// operation is idempotent: a second caller after the link is set is sent to
// the same rematch game. The original game must be finished.
func (s *Store) Rematch(ctx context.Context, originalID string) (*GameRecord, error) {
	orig, ok, err := s.Get(ctx, originalID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
	}

	orig.Lock()
	if orig.Status != StatusFinished {
		orig.Unlock()
		return nil, ErrNotFinished
	}
	if linked := orig.RematchGameID; linked != "" {
		orig.Unlock()
		// Another caller already created the rematch — fetch and return it.
		rec, ok, err := s.Get(ctx, linked)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The link points to a game that no longer exists (rare —
			// ON DELETE SET NULL handles the FK at the DB layer, but the
			// in-memory copy could still hold a stale ID). Treat as
			// "no rematch yet" and create a new one.
		} else {
			return rec, nil
		}
	}
	numPlayers := len(orig.Seats)
	vis := orig.Visibility
	origCfg := orig.State.Config
	orig.Unlock()

	// Build the new game using the same shape as Create. We don't call Create
	// directly because we want to atomically write the rematch_game_id link on
	// the original game alongside the new game's row. Carry the original
	// game's clock settings through ConfigFor so a rematch inherits the prior
	// time control (rules are at the rematch's player count, clock is the
	// same as last time).
	colors := make([]game.Color, numPlayers)
	seats := make([]Seat, numPlayers)
	for i := 0; i < numPlayers; i++ {
		colors[i] = game.Color(i + 1)
		seats[i] = Seat{Index: i, Color: colors[i]}
	}
	rec := &GameRecord{
		ID:          newID(),
		State:       game.NewGame(colors, game.ConfigFor(numPlayers, origCfg)),
		Seats:       seats,
		Status:      StatusWaiting,
		Visibility:  vis,
		CreatedAt:   time.Now(),
		DrawOfferBy: -1,
	}
	if err := s.repo.SaveNewGame(ctx, rec); err != nil {
		return nil, err
	}

	// Link original → new. If two goroutines raced past the early-out above,
	// the repo's SetRematchLink resolves the race: the loser observes that the
	// link is already set and returns the winner's game ID.
	winnerID, err := s.repo.SetRematchLink(ctx, originalID, rec.ID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if existing, found := s.games[winnerID]; found && winnerID != rec.ID {
		// Lost the race — discard the freshly-built record and return the
		// winner's. The orphaned `rec` row remains in the DB but is unlinked;
		// it'll age out via normal cleanup paths.
		s.mu.Unlock()
		orig.Lock()
		orig.RematchGameID = winnerID
		orig.Unlock()
		return existing, nil
	}
	if winnerID == rec.ID {
		s.games[rec.ID] = rec
	}
	s.mu.Unlock()

	orig.Lock()
	orig.RematchGameID = winnerID
	orig.Unlock()

	if winnerID != rec.ID {
		// Race lost but the cache didn't have the winner — fetch through Get.
		winner, _, err := s.Get(ctx, winnerID)
		if err != nil {
			return nil, err
		}
		return winner, nil
	}
	return rec, nil
}

// Get fetches a game, falling back to the repo if it isn't cached. Returns
// (nil, false, nil) if no such game exists anywhere.
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
		idx = firstFreeSeat(rec.Seats)
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
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return game.MoveResult{}, nil, err
	}
	if !ok {
		return game.MoveResult{}, nil, ErrGameNotFound
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
	rec.DrawOfferBy = -1

	if perr := s.repo.AppendMove(ctx, gameID, ordinal, move, rec.State.Winner, rec.State.WinKind, rec.Status); perr != nil {
		// We've already mutated in-memory state; returning the persist error
		// means the client retries and the DB stays out of sync. That's worse
		// than the inconsistency. Surface it but keep the in-memory truth.
		return res, rec, perr
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
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
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
	if _, ok := rec.SeatByToken(token); !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	rec.Unlock()
	if err := s.startInternal(rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *Store) AddBot(ctx context.Context, gameID string, seatIdx int) (*GameRecord, error) {
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
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

// Matchmake returns a public waiting game of the requested player count
// with at least one free seat that `excludeUserID` is *not* already seated
// in. For 2-player (1v1) games we score candidates by Elo proximity: the
// closest-rated candidate that falls within the room's age-widened
// tolerance band wins. For 3+ player (multi) we keep the simpler "join the
// most-filled room" model — the auto-promoter starts those when enough
// players have accumulated, so rating-pairing within a multi room would
// only delay the start without changing who plays.
//
// If no candidate matches, a fresh public game is created and the caller
// becomes its first seat-holder.
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
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
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
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
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
		// In-memory truth wins (same policy as handleFlag) — log the persist
		// failure but don't roll the engine back.
		_ = err
	}
	if hadOffer && s.onDrawOffer != nil {
		s.onDrawOffer(gameID, -1)
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
func (s *Store) OfferDraw(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
	}

	rec.Lock()
	if len(rec.Seats) != 2 {
		rec.Unlock()
		return rec, ErrDrawUnsupported
	}
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.DrawOfferBy >= 0 {
		if rec.DrawOfferBy == seat.Index {
			// Re-offering by the same player is a no-op — return success so
			// the UI's optimistic state matches the server.
			rec.Unlock()
			return rec, nil
		}
		// The opponent is already offering; the right action is "accept",
		// not "offer". Surface this distinction to the caller.
		rec.Unlock()
		return rec, ErrDrawAlreadyOffered
	}
	rec.DrawOfferBy = seat.Index
	offeredBy := seat.Index
	rec.Unlock()

	// Listener may need to re-enter the record (via store.Get → toGameDTO),
	// so we MUST release rec.Lock before firing it. Same pattern as Resign.
	if s.onDrawOffer != nil {
		s.onDrawOffer(gameID, offeredBy)
	}
	return rec, nil
}

// AcceptDraw ends the game in a draw if the *opponent* has an offer pending.
// Self-acceptance is rejected. 2-player only.
func (s *Store) AcceptDraw(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
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
	if rec.DrawOfferBy < 0 {
		rec.Unlock()
		return rec, ErrDrawNotOffered
	}
	if rec.DrawOfferBy == seat.Index {
		rec.Unlock()
		return rec, ErrCannotAcceptOwnDrawOffer
	}
	rec.State.AgreeDraw()
	rec.Status = StatusFinished
	rec.DrawOfferBy = -1
	winner := rec.State.Winner
	winKind := rec.State.WinKind
	rec.Unlock()

	s.gameEnded(gameID)
	if err := s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind); err != nil {
		_ = err
	}
	if s.onDrawOffer != nil {
		s.onDrawOffer(gameID, -1)
	}
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
	return rec, nil
}

// DeclineDraw clears a pending draw offer. Either the opponent (refusing the
// offer) or the offering player (withdrawing it) may call this — we don't
// distinguish them at the wire level.
func (s *Store) DeclineDraw(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
	}

	rec.Lock()
	_, ok = rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.DrawOfferBy < 0 {
		rec.Unlock()
		return rec, ErrDrawNotOffered
	}
	rec.DrawOfferBy = -1
	rec.Unlock()

	if s.onDrawOffer != nil {
		s.onDrawOffer(gameID, -1)
	}
	return rec, nil
}

// maybeScheduleBot inspects the current state of `rec` and, if it is a bot's
// turn, kicks off a goroutine that will play one move after `s.botDelay`.
// Safe to call whether or not the caller holds rec.Lock — the goroutine does
// its own locking.
func (s *Store) maybeScheduleBot(rec *GameRecord) {
	if rec == nil {
		return
	}
	go func() {
		if s.botDelay > 0 {
			time.Sleep(s.botDelay)
		}
		s.playBotTurnIfNeeded(rec)
	}()
}

// playBotTurnIfNeeded plays exactly one move on behalf of the active bot, if
// the game is still in progress and the active player is a bot. After the
// move it recursively schedules the next turn — which means bot-vs-bot games
// progress automatically without external prodding.
func (s *Store) playBotTurnIfNeeded(rec *GameRecord) {
	rec.Lock()
	if rec.Status != StatusPlaying || rec.State.IsOver() {
		rec.Unlock()
		return
	}
	seatIdx := rec.State.Turn
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return
	}
	seat := &rec.Seats[seatIdx]
	if !seat.IsBot {
		rec.Unlock()
		return
	}
	color := seat.Color
	pos, ok := s.botEngine.BestMove(rec.State, color)
	if !ok {
		rec.Unlock()
		return
	}

	ordinal := len(rec.State.History)
	move := game.Move{Player: color, Pos: pos}
	res, err := rec.State.ApplyMove(move, time.Now())
	if err != nil {
		// BestMove returned an illegal position (shouldn't happen — we only
		// consider Empty in-bounds cells), or the engine rejected for some
		// other reason. Skip rather than crash: a human player can still
		// resign or wait for a flag.
		rec.Unlock()
		return
	}
	if rec.State.IsOver() {
		rec.Status = StatusFinished
	}
	rec.DrawOfferBy = -1 // any draw offer is auto-rescinded by a move

	winner := rec.State.Winner
	winKind := rec.State.WinKind
	status := rec.Status
	gameID := rec.ID
	s.armClock(rec)
	rec.Unlock()

	// Persist with the same shape PlayMove uses — through AppendMove for
	// the move log + status update. We don't have a request context here
	// since this is server-driven; Background is fine.
	if err := s.repo.AppendMove(context.Background(), gameID, ordinal, move, winner, winKind, status); err != nil {
		// In-memory truth wins (same policy as the human path).
		_ = err
	}

	if s.onMove != nil {
		s.onMove(gameID, res)
	}
	// Continue the chain — if the next active player is also a bot, this
	// schedules another move; if it's a human's turn, this is a no-op.
	s.maybeScheduleBot(rec)
}

// maybeApplyRating is called every time a game's Status flips to Finished.
// It runs the Elo update asynchronously: eligibility is checked under the
// rec.Lock, then the persistence happens in its own goroutine because
// ApplyRatedGame opens a DB transaction we don't want serialised behind the
// game lock. Idempotency lives in the repo (UPDATE … WHERE rated_at IS NULL),
// so calling this twice from different code paths is safe.
//
// Eligible games: visibility=public AND every seat is an authenticated
// human (no bots, no anonymous). 2-player games update the 1v1 rating
// pool; 3+ player games update the multi pool via the moyenne-des-
// adversaires zero-sum extension (see elo.UpdateMulti). Games that end
// without a winner (multi timeout — Winner == Empty) are skipped: there's
// nothing to credit.
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
		{UserID: a.UserID, NewRating: elo.Update(rA, rB, outcomeA), Result: resultA},
		{UserID: b.UserID, NewRating: elo.Update(rB, rA, outcomeB), Result: resultB},
	}
	// Every rated player must have a profile row so the leaderboard's
	// INNER JOIN doesn't silently drop them. The seat name is what
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
	if _, err := s.repo.ApplyRatedGame(ctx, gameID, RatingMode1v1, updates); err != nil {
		slog.Default().Error("apply rated game (1v1)", "game", gameID, "err", err)
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
	updates := make([]RatingUpdate, len(results))
	for i, r := range results {
		updates[i] = RatingUpdate{UserID: r.UserID, NewRating: r.NewRating, Result: r.Result}
	}
	// Same belt-and-suspenders as applyRating1v1: every rated player
	// needs a profiles row or the leaderboard INNER JOIN drops them.
	for _, st := range seats {
		if err := s.repo.EnsureProfile(ctx, st.UserID, st.Name); err != nil {
			slog.Default().Error("ensure profile (rated multi)", "user", st.UserID, "err", err)
		}
	}
	if _, err := s.repo.ApplyRatedGame(ctx, gameID, RatingModeMulti, updates); err != nil {
		slog.Default().Error("apply rated game (multi)", "game", gameID, "err", err)
	}
}

func ratingOrDefault(r Rating) int {
	if r.Games == 0 {
		return elo.DefaultRating
	}
	return r.Rating
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

	rec, ok, err := s.Get(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
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
