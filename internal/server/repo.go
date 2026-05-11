package server

import (
	"context"

	"github.com/alexis/gemline/internal/game"
)

// Repository persists game and user state. Implementations may be backed by
// a database (PostgresRepo) or be a no-op for tests.
//
// The model is event-sourced for moves: only the metadata + move log is
// stored, and GameState is rebuilt by replaying moves through ApplyMove.
type Repository interface {
	SaveNewGame(ctx context.Context, rec *GameRecord) error
	LoadGame(ctx context.Context, id string) (*GameRecord, error)
	UpdateSeat(ctx context.Context, gameID string, seat *Seat, status Status) error
	AppendMove(ctx context.Context, gameID string, ordinal int, m game.Move, winner game.Color, winKind game.WinKind, status Status) error

	// Profile returns the profile row for userID, or (nil, nil) if there
	// isn't one yet.
	Profile(ctx context.Context, userID string) (*Profile, error)

	// UpsertProfile creates or updates the profile row for userID.
	UpsertProfile(ctx context.Context, userID, displayName string) error

	// GamesForUser returns the most recent games where userID held a seat,
	// most recent first, capped at `limit`.
	GamesForUser(ctx context.Context, userID string, limit int) ([]UserGame, error)

	// StatsForUser returns aggregate counts across all of the user's games.
	StatsForUser(ctx context.Context, userID string) (UserStats, error)
}

// Profile is the user-controlled profile row.
type Profile struct {
	UserID      string
	DisplayName string
}

// UserGame summarises one game a user took part in, for the history view.
type UserGame struct {
	GameID     string     `json:"gameId"`
	Status     Status     `json:"status"`
	SeatIndex  int        `json:"seatIndex"`
	Color      game.Color `json:"color"`
	WinnerColor game.Color `json:"winnerColor"`
	Outcome    string     `json:"outcome"` // "won", "lost", "ongoing"
	MoveCount  int        `json:"moveCount"`
	CreatedAt  string     `json:"createdAt"`
	UpdatedAt  string     `json:"updatedAt"`
}

// UserStats are aggregate counts derived from the user's finished games.
type UserStats struct {
	Total   int `json:"total"`
	Won     int `json:"won"`
	Lost    int `json:"lost"`
	Ongoing int `json:"ongoing"`
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
func (noopRepo) Profile(context.Context, string) (*Profile, error)        { return nil, nil }
func (noopRepo) UpsertProfile(context.Context, string, string) error      { return nil }
func (noopRepo) GamesForUser(context.Context, string, int) ([]UserGame, error) {
	return nil, nil
}
func (noopRepo) StatsForUser(context.Context, string) (UserStats, error) {
	return UserStats{}, nil
}
