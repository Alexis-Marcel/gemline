package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

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

	onState    func(gameID string)                             // state changed (forfeit, etc.)
	onPresence func(gameID string, seatIndex int, online bool) // presence transition
}

func NewStore(repo Repository) *Store {
	if repo == nil {
		repo = noopRepo{}
	}
	return &Store{
		games:    make(map[string]*GameRecord),
		repo:     repo,
		clocks:   newClockManager(),
		presence: newPresenceManager(),
		seatRefs: make(map[string]map[int]int),
	}
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

// Close stops every running clock + presence timer. Call on shutdown.
func (s *Store) Close() {
	s.clocks.CancelAll()
	s.presence.CancelAll()
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
	s.presence.Schedule(gameID, seatIndex, DisconnectGracePeriod, func() {
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

	s.clocks.Cancel(gameID)
	s.presence.CancelGame(gameID)
	_ = s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind)
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

	s.presence.CancelGame(gameID)
	if err := s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind); err != nil {
		// Persistence failed but the in-memory state is the truth.
		// We do not retry; an admin can inspect the row if needed.
	}
	if s.onState != nil {
		s.onState(gameID)
	}
}

// Create initializes a game, persists it, and caches it in memory. Visibility
// controls whether the game is discoverable through the public lobby; passing
// the zero value defaults to private.
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
		ID:         newID(),
		State:      game.NewGame(colors, game.DefaultConfig(numPlayers)),
		Seats:      seats,
		Status:     StatusWaiting,
		Visibility: vis,
		CreatedAt:  time.Now(),
	}
	if err := s.repo.SaveNewGame(ctx, rec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.games[rec.ID] = rec
	s.mu.Unlock()
	return rec, nil
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

// ListLobby returns the most recently created public waiting games, capped at
// limit. The repo (when DB-backed) is the source of truth and returns an
// empty-but-non-nil slice for "no public waiting games"; a nil return signals
// "no persistence wired up at all" (noopRepo) — in that case we fall back to
// scanning the in-memory cache so single-process hermetic runs still work.
func (s *Store) ListLobby(ctx context.Context, limit int) ([]LobbyEntry, error) {
	entries, err := s.repo.LobbyGames(ctx, limit)
	if err != nil {
		return nil, err
	}
	if entries != nil {
		return entries, nil
	}
	return s.scanLobbyCache(limit), nil
}

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
	orig.Unlock()

	// Build the new game using the same shape as Create. We don't call Create
	// directly because we want to atomically write the rematch_game_id link on
	// the original game alongside the new game's row.
	colors := make([]game.Color, numPlayers)
	seats := make([]Seat, numPlayers)
	for i := 0; i < numPlayers; i++ {
		colors[i] = game.Color(i + 1)
		seats[i] = Seat{Index: i, Color: colors[i]}
	}
	rec := &GameRecord{
		ID:         newID(),
		State:      game.NewGame(colors, game.DefaultConfig(numPlayers)),
		Seats:      seats,
		Status:     StatusWaiting,
		Visibility: vis,
		CreatedAt:  time.Now(),
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

	if perr := s.repo.AppendMove(ctx, gameID, ordinal, move, rec.State.Winner, rec.State.WinKind, rec.Status); perr != nil {
		// We've already mutated in-memory state; returning the persist error
		// means the client retries and the DB stays out of sync. That's worse
		// than the inconsistency. Surface it but keep the in-memory truth.
		return res, rec, perr
	}

	// Re-arm or cancel the chess clock for the next player. Called while
	// rec is still locked — armClock doesn't take the rec lock itself.
	s.armClock(rec)
	return res, rec, nil
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
