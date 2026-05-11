package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
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

var (
	ErrGameNotFound = errors.New("game not found")
	ErrSeatTaken    = errors.New("seat already taken")
	ErrNoFreeSeat   = errors.New("no free seat")
	ErrBadToken     = errors.New("invalid player token")
	ErrNotPlaying   = errors.New("game is not in playing state")
)

// Seat is a play slot in a game. Once claimed, only the SHA-256 of the
// player token lives in TokenHash — the plaintext token is returned exactly
// once, when the seat is claimed, and is never persisted.
type Seat struct {
	Index     int        // 0..N-1, also turn order
	Color     game.Color // C1..C6
	Name      string
	TokenHash []byte
	Occupied  bool
	IsBot     bool
}

type GameRecord struct {
	mu        sync.Mutex
	ID        string
	State     *game.GameState
	Seats     []Seat
	Status    Status
	CreatedAt time.Time
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
// of truth for state that has to outlive a restart.
type Store struct {
	mu    sync.Mutex
	games map[string]*GameRecord
	repo  Repository
}

func NewStore(repo Repository) *Store {
	if repo == nil {
		repo = noopRepo{}
	}
	return &Store{games: make(map[string]*GameRecord), repo: repo}
}

// Create initializes a game, persists it, and caches it in memory.
func (s *Store) Create(ctx context.Context, numPlayers int) (*GameRecord, error) {
	colors := make([]game.Color, numPlayers)
	seats := make([]Seat, numPlayers)
	for i := 0; i < numPlayers; i++ {
		colors[i] = game.Color(i + 1)
		seats[i] = Seat{Index: i, Color: colors[i]}
	}
	rec := &GameRecord{
		ID:        newID(),
		State:     game.NewGame(colors, game.DefaultConfig(numPlayers)),
		Seats:     seats,
		Status:    StatusWaiting,
		CreatedAt: time.Now(),
	}
	if err := s.repo.SaveNewGame(ctx, rec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.games[rec.ID] = rec
	s.mu.Unlock()
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
	if existing, found := s.games[id]; found {
		loaded = existing
	} else {
		s.games[id] = loaded
	}
	s.mu.Unlock()
	return loaded, true, nil
}

// Join claims a seat in `gameID` for `name`. If seatIdx is negative, the
// first free seat is chosen. Returns the claimed seat and the plaintext
// player token (only available here — only its hash is persisted).
func (s *Store) Join(ctx context.Context, gameID, name string, seatIdx int) (*Seat, string, error) {
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
	rec.Seats[idx].Occupied = true
	if rec.AllSeated() {
		rec.Status = StatusPlaying
	}

	if err := s.repo.UpdateSeat(ctx, gameID, &rec.Seats[idx], rec.Status); err != nil {
		return nil, "", err
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
	res, err := rec.State.ApplyMove(move)
	if err != nil {
		return res, rec, err
	}
	if rec.State.Winner != game.Empty {
		rec.Status = StatusFinished
	}

	if perr := s.repo.AppendMove(ctx, gameID, ordinal, move, rec.State.Winner, rec.State.WinKind, rec.Status); perr != nil {
		// We've already mutated in-memory state; returning the persist error
		// means the client retries and the DB stays out of sync. That's worse
		// than the inconsistency. Surface it but keep the in-memory truth.
		return res, rec, perr
	}
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
