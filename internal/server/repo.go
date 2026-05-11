package server

import (
	"context"

	"github.com/alexis/gemline/internal/game"
)

// Repository persists game state. Implementations may be backed by a database
// (PostgresRepo) or be a no-op for tests.
//
// The model is event-sourced for moves: only the metadata + move log is
// stored, and GameState is rebuilt by replaying moves through ApplyMove.
type Repository interface {
	// SaveNewGame persists a fresh game (with all seats unclaimed and no
	// moves yet). Idempotent if the id already exists is not guaranteed —
	// callers should not retry SaveNewGame with the same id.
	SaveNewGame(ctx context.Context, rec *GameRecord) error

	// LoadGame fetches the full game state by replaying its move log. Returns
	// (nil, nil) if no game with that id exists. The returned record is
	// detached from any in-memory cache; callers must take care of caching.
	LoadGame(ctx context.Context, id string) (*GameRecord, error)

	// UpdateSeat persists a seat claim (name, token hash, occupied) and the
	// resulting game status transition.
	UpdateSeat(ctx context.Context, gameID string, seat *Seat, status Status) error

	// AppendMove persists the n-th move (zero-indexed ordinal). It also
	// updates the game's win state and status if the move ended the game.
	AppendMove(ctx context.Context, gameID string, ordinal int, m game.Move, winner game.Color, winKind game.WinKind, status Status) error
}

// noopRepo lets the in-memory Store run without a database. It returns
// ErrNoGame for any load — callers should treat that as "not found" and
// fall back to whatever they have in memory.
type noopRepo struct{}

func (noopRepo) SaveNewGame(context.Context, *GameRecord) error { return nil }
func (noopRepo) LoadGame(context.Context, string) (*GameRecord, error) {
	return nil, nil
}
func (noopRepo) UpdateSeat(context.Context, string, *Seat, Status) error { return nil }
func (noopRepo) AppendMove(context.Context, string, int, game.Move, game.Color, game.WinKind, Status) error {
	return nil
}
