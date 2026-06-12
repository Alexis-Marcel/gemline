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

// Visibility is about lobby discovery, not access control: private games are
// still joinable by anyone holding the URL.
type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

var (
	ErrGameNotFound             = errors.New("game not found")
	ErrSeatTaken                = errors.New("seat already taken")
	ErrNoFreeSeat               = errors.New("no free seat")
	ErrBadToken                 = errors.New("invalid player token")
	ErrNotPlaying               = errors.New("game is not in playing state")
	ErrNotFinished              = errors.New("game is not finished")
	ErrBadVisibility            = errors.New("invalid visibility")
	ErrDrawNotOffered           = errors.New("no draw offer pending")
	ErrCannotAcceptOwnDrawOffer = errors.New("the offering player cannot accept their own draw offer")
	ErrDrawAlreadyOffered       = errors.New("a draw is already being offered")
	ErrDrawUnsupported          = errors.New("draw is only supported in 2-player games")
	ErrBotsOnPublic             = errors.New("bots cannot be added to public games")
	ErrSeatNotBot               = errors.New("seat is not occupied by a bot")
	ErrSeatReserved             = errors.New("seat is reserved for another player")
	ErrSeatNotInvited           = errors.New("seat has no pending invitation")
	ErrAnonymousOnPublic        = errors.New("public games require authentication to join")
	ErrBadSeatIndex             = errors.New("seat index out of range")
	ErrPublicCannotStart        = errors.New("public games start automatically; manual start is only for private games")
	ErrTooFewToStart            = errors.New("at least 2 seats must be occupied to start")
	ErrNoRematchOffer           = errors.New("no rematch offer pending")
	ErrNotInvitee               = errors.New("only the invited user can act on this invitation")
	ErrNotHost                  = errors.New("only the host can start this game")
	ErrSeatNotForUser           = errors.New("no seat reserved for this user in the game")
)

// Seat is a play slot. Only the SHA-256 of the player token is kept; the
// plaintext is returned once at claim time and never persisted.
type Seat struct {
	Index     int // also turn order
	Color     game.Color
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
	RematchGameID string
	CreatedAt     time.Time

	// DrawOfferBy is the seat index currently offering a draw, or -1 for none.
	DrawOfferBy int

	// RematchOffer is the pending rematch acceptance state, or nil.
	RematchOffer *RematchOffer
}

// RematchOffer is the per-seat acceptance state of a rematch proposal. The new
// game is created only once every needed human seat has accepted. Persisted as
// games.rematch_offer JSONB so a player's accept landing on another pod (after
// cache invalidation) completes the existing offer rather than starting a new
// one with only that player.
type RematchOffer struct {
	// AcceptedSeats maps seat index (in the finished game) to true; absent
	// means still pending. Bots are pre-marked at offer creation.
	AcceptedSeats map[int]bool `json:"acceptedSeats"`
	CreatedAt     time.Time    `json:"createdAt"`
}

func (r *GameRecord) Lock()   { r.mu.Lock() }
func (r *GameRecord) Unlock() { r.mu.Unlock() }

// SeatByToken matches on the token hash with constant-time compare to avoid
// timing leaks. Caller must hold the record lock.
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

// Store is the in-memory cache of games over a persistent repo (the DB is the
// source of truth across restarts). It also owns the chess clock: per-game
// timeout timers and the forfeits they fire.
type Store struct {
	mu       sync.Mutex
	games    map[string]*GameRecord
	repo     Repository
	clocks   *clockManager
	presence *presenceManager
	seatRefs map[string]map[int]int // gameID → seatIndex → live connections

	onState     func(gameID string)
	onPresence  func(gameID string, seatIndex int, online bool)
	onDrawOffer func(gameID string, offeredBy int) // offeredBy -1 when cleared
	onMove      func(gameID string, mv game.MoveResult)
	onRated     func(gameID string)

	// botEngine carries its own PRNG so concurrent BestMove calls don't fight
	// over a global random source.
	botEngine *ai.Engine
	// botDelay is artificial think-time before a bot plays; tests set it to 0.
	botDelay time.Duration

	// disconnectGrace is how long a seat may stay disconnected before
	// forfeiting; tests shorten it to exercise the timeout path.
	disconnectGrace time.Duration

	// cleanerStop halts the stale-game cleaner; nil until it's started.
	cleanerStop chan struct{}
}

func (s *Store) Repo() Repository { return s.repo }

// Invalidate drops the cache entry so the next Get reloads from the DB. Wired
// to the backplane listener: a NOTIFY from another pod invalidates here, so the
// cache never drifts more than one hop behind. Goroutines holding an existing
// rec pointer keep using it (and see stale data) until they call Get again.
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
		games:           make(map[string]*GameRecord),
		repo:            repo,
		clocks:          newClockManager(),
		presence:        newPresenceManager(),
		seatRefs:        make(map[string]map[int]int),
		botEngine:       ai.NewEngine(time.Now().UnixNano()),
		botDelay:        600 * time.Millisecond,
		disconnectGrace: DisconnectGracePeriod,
	}
}

// WithDisconnectGrace overrides the disconnect-grace timeout (for tests).
func (s *Store) WithDisconnectGrace(d time.Duration) *Store {
	s.disconnectGrace = d
	return s
}

// noteSwallowedErr logs and counts a persistence error the store treats as
// non-fatal (in-memory state wins). op must stay low-cardinality (Prom label).
func noteSwallowedErr(op string, err error) {
	if err == nil {
		return
	}
	persistErrorsTotal.WithLabelValues(op).Inc()
	slog.Default().Warn("store: persistence error", "op", op, "err", err)
}

// WithBotDelay overrides the bot's artificial think-time (tests pass 0).
func (s *Store) WithBotDelay(d time.Duration) *Store {
	s.botDelay = d
	return s
}

// SetMoveListener registers a callback to push bot-applied moves over the WS;
// human moves already broadcast through the HTTP handler.
func (s *Store) SetMoveListener(fn func(gameID string, mv game.MoveResult)) {
	s.onMove = fn
}

// SetStateListener registers a callback for Store-driven mutations outside the
// request path (clock flag, disconnect forfeit).
func (s *Store) SetStateListener(fn func(gameID string)) { s.onState = fn }

// SetPresenceListener registers a callback for seat online/offline flips.
func (s *Store) SetPresenceListener(fn func(gameID string, seatIndex int, online bool)) {
	s.onPresence = fn
}

// SetDrawOfferListener registers a callback for draw-offer changes; offeredBy
// is -1 when cleared.
func (s *Store) SetDrawOfferListener(fn func(gameID string, offeredBy int)) {
	s.onDrawOffer = fn
}

// SetRatedListener registers a callback fired once after the Elo update is
// persisted. Runs in maybeApplyRating's goroutine and must not block.
func (s *Store) SetRatedListener(fn func(gameID string)) {
	s.onRated = fn
}

// Close stops every clock, presence timer, and the stale-game cleaner.
func (s *Store) Close() {
	s.clocks.CancelAll()
	s.presence.CancelAll()
	if s.cleanerStop != nil {
		close(s.cleanerStop)
		s.cleanerStop = nil
	}
}

// StaleWaitingTTL is the age past which a waiting game is deleted as abandoned.
const StaleWaitingTTL = 7 * 24 * time.Hour

const staleCleanerInterval = 1 * time.Hour

// StartStaleGameCleaner ticks a background DELETE of waiting games older than
// StaleWaitingTTL. Safe on every pod (DELETE is idempotent) and against double
// invocation in one process.
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
		// Tick once immediately so a fresh pod doesn't wait an hour.
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

// startInternal is the auth-less path from waiting to playing: trim unoccupied
// seats, rebuild the engine for the remaining colours, persist, schedule the
// first bot move. Public matched games skip this — they're created in `playing`.
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
	// Win thresholds follow the actual seated count, not the created slot
	// count: a 6-seat room launched with 3 plays under the 3-player rulebook.
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

// armClock schedules (or cancels) the active player's timeout. Caller must
// hold rec.Lock.
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

// gameEnded is the shared post-finish cleanup: cancel grace timers, drop the
// seatRefs entry (else the map grows unbounded), release the clock slot.
// Idempotent across the various game-end paths.
func (s *Store) gameEnded(gameID string) {
	s.presence.CancelGame(gameID)
	s.clocks.Cancel(gameID)
	s.mu.Lock()
	delete(s.seatRefs, gameID)
	s.mu.Unlock()
}

// SeatConnected bumps the seat's connection refcount; on the 0→1 transition it
// cancels any pending disconnect-grace timer and notifies listeners.
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

// SeatDisconnected drops the refcount; on the 1→0 transition it starts a
// disconnect-grace timer that forfeits the player if it fires first.
func (s *Store) SeatDisconnected(gameID string, seatIndex int) {
	s.mu.Lock()
	if s.seatRefs[gameID] == nil {
		s.mu.Unlock()
		return
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

// SeatOnline reports whether the seat has at least one live connection.
func (s *Store) SeatOnline(gameID string, seatIndex int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.seatRefs[gameID]; ok {
		return m[seatIndex] > 0
	}
	return false
}

// handleDisconnectTimeout forfeits a seat whose grace period expired with no
// live connection.
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

// handleFlag forfeits the active player when their clock runs out.
func (s *Store) handleFlag(gameID string) {
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
	// Recheck: a move may have landed between the timer firing and the lock,
	// in which case the player must not be forfeited.
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
		// In-memory state is the truth; no retry.
	}
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
}

// Create initializes, persists, and caches a game. Zero-value vis defaults to
// private. Bots are added per-seat via AddBot afterward, not at create time.
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
		// Placeholder cfg: real thresholds are committed at Start once the
		// seated count is known; the DTO previews them while still waiting.
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

type LobbyEntry struct {
	GameID    string    `json:"gameId"`
	Players   int       `json:"players"`
	Seated    int       `json:"seated"`
	CreatedAt time.Time `json:"createdAt"`
}

// scanLobbyCache returns public waiting games from the in-memory cache,
// most-recent-first. Used by Matchmake under a noop repo (hermetic tests),
// where the cache is authoritative.
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
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// getOrNotFound is Get with a not-found result turned into ErrGameNotFound.
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

// Get returns the cached game, loading from the repo (and caching) on a miss.
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

	// Race-safe fill: if another goroutine loaded the same game first, keep
	// its copy so callers don't fork the in-memory state.
	s.mu.Lock()
	freshlyCached := false
	if existing, found := s.games[id]; found {
		loaded = existing
	} else {
		s.games[id] = loaded
		freshlyCached = true
	}
	s.mu.Unlock()

	if freshlyCached {
		loaded.Lock()
		s.armClock(loaded)
		loaded.Unlock()
	}
	return loaded, true, nil
}

// Join claims a seat (negative seatIdx auto-picks). userID is "" for a guest.
// Returns the claimed seat and the plaintext token (only its hash is persisted).
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
	// Reject anonymous public joins: with no stable identity there's nothing
	// to rate against, and a public slot must stay rateable.
	if rec.Visibility == VisibilityPublic && userID == "" {
		return nil, "", ErrAnonymousOnPublic
	}

	idx := seatIdx
	if idx < 0 {
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
		// Enforce the reservation: only the invited user may take their seat.
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
	// The active player on a fresh start may be a bot — kick its turn.
	if startedPlaying {
		go s.maybeScheduleBot(rec)
	}
	return &rec.Seats[idx], token, nil
}

// ResolveSeat issues a fresh token for the authed user's own occupied human
// seat — the single pull path by which a pre-seated player (matchmaking or
// rematch) obtains their credentials. Each call rotates the token.
func (s *Store) ResolveSeat(ctx context.Context, gameID, userID string) (*Seat, string, error) {
	if userID == "" {
		return nil, "", ErrSeatNotForUser
	}
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, "", err
	}

	rec.Lock()
	defer rec.Unlock()

	idx := -1
	for i := range rec.Seats {
		st := rec.Seats[i]
		if st.Occupied && !st.IsBot && st.UserID == userID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, "", ErrSeatNotForUser
	}

	token := newToken()
	rec.Seats[idx].TokenHash = hashToken(token)
	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[idx], rec.Status); err != nil {
		return nil, "", err
	}
	return &rec.Seats[idx], token, nil
}

// PlayMove authenticates the token, applies the move, persists it plus any
// win-state change, and returns the engine result.
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
	// Any move rescinds a pending draw offer. We don't fire the listener here:
	// the move broadcast already carries DrawOfferBy=-1, and re-entering Get
	// under the held rec.Lock would deadlock.
	hadDrawOffer := rec.DrawOfferBy >= 0
	rec.DrawOfferBy = -1

	if perr := s.repo.AppendMove(ctx, gameID, ordinal, move, rec.State.Winner, rec.State.WinKind, rec.Status); perr != nil {
		// State already mutated; a retry would desync the DB further, so keep
		// the in-memory truth and just surface the error.
		return res, rec, perr
	}
	if hadDrawOffer {
		// Clear draw_offer_by so other pods don't reload the stale offerer.
		// Skipped when none was pending to avoid a write per move.
		if perr := s.repo.SaveDrawOffer(ctx, gameID, -1); perr != nil {
			return res, rec, perr
		}
	}

	s.armClock(rec)
	// Bot's PlayMove takes rec.Lock (held by the deferred Unlock), so defer.
	defer s.maybeScheduleBot(rec)
	// On game end, run the shared cleanup + rating via goroutines: defer LIFO
	// would run them before the Unlock above, and they reach for s.mu / rec.Lock.
	if rec.State.IsOver() {
		gid := gameID
		go func() {
			s.gameEnded(gid)
			s.maybeApplyRating(rec)
		}()
	}
	return res, rec, nil
}

// Start finalises a private waiting game: drop unoccupied seats, rebuild the
// engine for the remaining players, flip to playing, start the clock. The
// caller's seat token authorises it. Rejects public games (matchmaking
// auto-starts those) and games with fewer than 2 occupied seats.
func (s *Store) Start(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	// Auth/visibility checks live here; trimming + persistence is shared with
	// startInternal.
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
	// Host-only: only seat 0 (the creator) may start, so a guest can't race
	// the host while they're still filling the lobby.
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

// AddBot claims an empty seat with a bot. Private games only — public games
// must be filled by humans through matchmaking.
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
		s.maybeScheduleBot(rec)
	}
	return rec, nil
}

// RemoveBot vacates a bot-occupied seat in a private waiting game (inverse of
// AddBot). Humans leave via LeaveSeat with their own token instead.
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

// InviteSeat reserves a seat for a named user in a private waiting game. The
// seat stays Occupied=false until that user joins via the game URL; Join then
// prefers the reserved seat and bars others from it. No caller credentials:
// the private URL is the shared secret.
func (s *Store) InviteSeat(ctx context.Context, gameID string, seatIdx int, inviteeID, inviteeName string) (*GameRecord, error) {
	if inviteeID == "" || inviteeName == "" {
		return nil, ErrBadSeatIndex
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

	// Occupied stays false so AllSeated() doesn't flip the game to playing on
	// an invite; the token is minted at actual join time.
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

// CancelSeatInvite clears a pending invitation back to an empty seat. The
// "carries an invitation" guard stops the endpoint from kicking humans or bots.
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

// DeclineSeatInvite is the invitee-side CancelSeatInvite: same empty end-state,
// but only the invited userID may call it.
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

// Matchmake — TEST FIXTURE ONLY; production uses the async queue + matcher
// tick. Returns a joinable public waiting game (excludeUserID not already
// seated), scoring 1v1 by age-widened Elo proximity and multi by most-filled /
// oldest-first; creates a fresh game if none match.
func (s *Store) Matchmake(ctx context.Context, players int, excludeUserID string) (*GameRecord, error) {
	candidates, err := s.repo.LobbyGames(ctx, 50)
	if err != nil {
		return nil, err
	}
	if candidates == nil {
		candidates = s.scanLobbyCache(50)
	}
	ratingMode := RatingModeMulti
	if players == 2 {
		ratingMode = RatingMode1v1
	}
	callerRating := s.fetchCallerRating(ctx, excludeUserID, ratingMode)

	type scored struct {
		rec    *GameRecord
		delta  float64
		age    time.Duration
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
			// 1v1: smallest rating delta. Multi: most-filled, age tiebreak.
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

// fetchCallerRating resolves a user's Elo for mode, defaulting when they have
// no rated games (or no user).
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

// averageRating returns the mean Elo of users in mode, unrated taking the
// default. Empty list returns the default.
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

// LeaveSeat frees the seat behind token, clearing it for reuse. Waiting games
// only — leaving an in-play game is Resign.
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

// Resign ends an in-progress game with the seat behind token giving up
// (WinResign; the survivor wins in 2-player). Persists and notifies listeners.
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
	hadOffer := rec.DrawOfferBy >= 0
	rec.DrawOfferBy = -1
	rec.Unlock()

	s.gameEnded(gameID)
	if err := s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind); err != nil {
		noteSwallowedErr("resign_outcome_persist", err)
	}
	if hadOffer {
		// Best-effort clear to other pods; failure doesn't roll back the resign.
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

// maybeApplyRating applies the Elo update for an eligible finished game (public,
// all authed humans). Runs the math in a goroutine.
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
	// Bail on any bot/anon seat: rating a mixed game would corrupt Elo.
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

// applyRating1v1 is the pairwise Elo update for mode='1v1'. A draw bumps both
// players' games/draws with zero net delta.
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
	// EnsureProfile first so the row exists before the leaderboard JOIN sees
	// the new rating. Errors are non-fatal: rating still applies.
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

// applyRatingMulti applies the zero-sum multi extension: the winner gains Elo
// measured against the field average, split evenly among the losers. Games with
// no winner are skipped by the caller.
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

	// Preserve opponents' seat order so RatingUpdate.UserID lines up with the
	// right new rating.
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
		return
	}

	results := elo.UpdateMulti(winnerSeat.UserID, winnerRating, oppIDs, oppRatings)
	// Index old ratings by user_id so each update's OldRating is filled without
	// redoing the winner/opponent split.
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
	// Every rated player needs a profiles row or the leaderboard JOIN drops them.
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

func (s *Store) RatingsForGame(ctx context.Context, gameID string) (GameRatings, error) {
	return s.repo.RatingsForGame(ctx, gameID)
}

// Leaderboard returns the top-rated players for mode.
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

// pickSeatForUser prefers a seat reserved for userID, else any unreserved empty
// seat; -1 if none is claimable. Anonymous joiners only get unreserved seats.
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

// hashToken returns the SHA-256 digest of a bearer token; the plaintext is
// never stored and compares happen on the hash.
func hashToken(tok string) []byte {
	sum := sha256.Sum256([]byte(tok))
	return sum[:]
}

// Per-user reads (Profile, UpsertProfile, GamesForUser, StatsForUser) bypass
// the per-game cache and go straight to the repo.

func (s *Store) Profile(ctx context.Context, userID string) (*Profile, error) {
	return s.repo.Profile(ctx, userID)
}

func (s *Store) UpsertProfile(ctx context.Context, userID, displayName string) error {
	return s.repo.UpsertProfile(ctx, userID, displayName)
}

// EnsureProfile creates a profile row if absent, never clobbering a chosen
// name. Used at rating-apply and on auth-me so every rated user has a row.
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

// PostMessage authenticates the seat token and appends a chat message; the body
// is trimmed and capped at MaxMessageLength.
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
