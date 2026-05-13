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

	// UpdateOutcome persists a state change that did NOT come from a move —
	// e.g. a clock-driven forfeit. The move log stays untouched.
	UpdateOutcome(ctx context.Context, gameID string, status Status, winner game.Color, winKind game.WinKind) error

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

	// AppendMessage persists a chat message and returns its DB-assigned id
	// and sent_at timestamp.
	AppendMessage(ctx context.Context, m *Message) error

	// MessagesForGame returns the most recent messages in `gameID`, oldest
	// first, up to `limit`.
	MessagesForGame(ctx context.Context, gameID string, limit int) ([]Message, error)

	// LobbyGames returns public games still in `waiting`, most recent first,
	// capped at `limit`. Each entry exposes only the metadata the lobby
	// renders — no seat tokens, no chat.
	LobbyGames(ctx context.Context, limit int) ([]LobbyEntry, error)

	// SetRematchLink writes rematch_game_id on `originalID` *if and only if*
	// it isn't set yet. Returns the rematch game ID that's now associated
	// with the original (either `newID` on success, or whatever ID was
	// already there on a lost race). This is the source of truth used by
	// Store.Rematch to resolve concurrent rematch calls.
	SetRematchLink(ctx context.Context, originalID, newID string) (string, error)
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

// Message is a chat line posted in a game. AuthorColor/AuthorName are
// denormalised snapshots captured at post time.
type Message struct {
	ID          int64      `json:"id"`
	GameID      string     `json:"gameId"`
	SeatIndex   int        `json:"seatIndex"`
	AuthorColor game.Color `json:"authorColor"`
	AuthorName  string     `json:"authorName"`
	Body        string     `json:"body"`
	SentAt      string     `json:"sentAt"`
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
func (noopRepo) UpdateOutcome(context.Context, string, Status, game.Color, game.WinKind) error {
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
func (noopRepo) AppendMessage(context.Context, *Message) error { return nil }
func (noopRepo) MessagesForGame(context.Context, string, int) ([]Message, error) {
	return nil, nil
}
func (noopRepo) LobbyGames(context.Context, int) ([]LobbyEntry, error) {
	return nil, nil
}
func (noopRepo) SetRematchLink(_ context.Context, _, newID string) (string, error) {
	// No DB → no link tracking. Returning newID keeps Store.Rematch's
	// idempotency contract intact for single-process, in-memory runs.
	return newID, nil
}
