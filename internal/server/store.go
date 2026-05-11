package server

import (
	"crypto/rand"
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
	ErrSeatTaken   = errors.New("seat already taken")
	ErrNoFreeSeat  = errors.New("no free seat")
	ErrBadToken    = errors.New("invalid player token")
	ErrNotPlaying  = errors.New("game is not in playing state")
)

type Seat struct {
	Index    int        // 0..N-1, also turn order
	Color    game.Color // C1..C6
	Name     string     // display name (optional)
	Token    string     // secret, returned only to the claiming client
	Occupied bool
	IsBot    bool
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

// SeatByToken returns the seat matching `tok` (without locking).
func (r *GameRecord) SeatByToken(tok string) (*Seat, bool) {
	for i := range r.Seats {
		if r.Seats[i].Token != "" && r.Seats[i].Token == tok {
			return &r.Seats[i], true
		}
	}
	return nil, false
}

// AllSeated reports whether every seat is occupied.
func (r *GameRecord) AllSeated() bool {
	for _, s := range r.Seats {
		if !s.Occupied {
			return false
		}
	}
	return true
}

type Store struct {
	mu    sync.RWMutex
	games map[string]*GameRecord
}

func NewStore() *Store {
	return &Store{games: make(map[string]*GameRecord)}
}

// Create initializes a game with `numPlayers` seats. The game starts in
// `waiting` state and transitions to `playing` once all seats are claimed.
func (s *Store) Create(numPlayers int) *GameRecord {
	colors := make([]game.Color, numPlayers)
	for i := 0; i < numPlayers; i++ {
		colors[i] = game.Color(i + 1)
	}
	seats := make([]Seat, numPlayers)
	for i := 0; i < numPlayers; i++ {
		seats[i] = Seat{Index: i, Color: colors[i]}
	}
	rec := &GameRecord{
		ID:        newID(),
		State:     game.NewGame(colors, game.DefaultConfig(numPlayers)),
		Seats:     seats,
		Status:    StatusWaiting,
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.games[rec.ID] = rec
	s.mu.Unlock()
	return rec
}

func (s *Store) Get(id string) (*GameRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.games[id]
	return rec, ok
}

// Join claims the first free seat (or the requested seat index if seatIdx >= 0).
// Returns the seat (with its token) on success. Caller must hold rec.Lock.
func Join(rec *GameRecord, name string, seatIdx int) (*Seat, error) {
	if rec.Status != StatusWaiting {
		return nil, ErrNotPlaying
	}
	if seatIdx >= 0 {
		if seatIdx >= len(rec.Seats) {
			return nil, ErrNoFreeSeat
		}
		if rec.Seats[seatIdx].Occupied {
			return nil, ErrSeatTaken
		}
		assignSeat(&rec.Seats[seatIdx], name)
	} else {
		idx := -1
		for i := range rec.Seats {
			if !rec.Seats[i].Occupied {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, ErrNoFreeSeat
		}
		assignSeat(&rec.Seats[idx], name)
		seatIdx = idx
	}
	if rec.AllSeated() {
		rec.Status = StatusPlaying
	}
	return &rec.Seats[seatIdx], nil
}

func assignSeat(s *Seat, name string) {
	s.Occupied = true
	s.Name = name
	s.Token = newToken()
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
