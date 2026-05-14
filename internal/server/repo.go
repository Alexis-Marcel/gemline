package server

import (
	"context"
	"encoding/json"
	"time"

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

	// RatingFor returns the rating row for (userID, mode), or a zero-value
	// Rating when no row exists yet. Callers must apply elo.DefaultRating
	// themselves when Rating.Games == 0.
	RatingFor(ctx context.Context, userID, mode string) (Rating, error)

	// RatingsFor returns rating rows for `userIDs` in the given mode, same
	// order; missing rows are returned as zero-value Rating entries.
	RatingsFor(ctx context.Context, userIDs []string, mode string) ([]Rating, error)

	// ApplyRatedGame atomically marks `gameID` as rated (via
	// `UPDATE games SET rated_at = NOW() WHERE rated_at IS NULL RETURNING`)
	// and persists the upserted rating rows for the supplied mode. Returns
	// true if this call won the race and applied the rating; false if the
	// game was already rated (no-op). Both updates and the games row run
	// in a single transaction so a crash mid-write can't leave one rating
	// bumped and the other untouched.
	ApplyRatedGame(ctx context.Context, gameID, mode string, updates []RatingUpdate) (bool, error)

	// Leaderboard returns the top-`limit` rated users for the given mode
	// (rating DESC), joined with their display name. Users with no profile
	// row are omitted — they're not visible enough to surface on a board.
	Leaderboard(ctx context.Context, mode string, limit int) ([]LeaderboardEntry, error)

	// FinalizeStart deletes every unoccupied seat row for `gameID` (the
	// host clicked Start and chose to play with fewer than max players),
	// updates the win-condition thresholds to match the actually-seated
	// player count (rules are decided at start time, not create time), and
	// flips the game's status to `status` — all in a single transaction.
	FinalizeStart(ctx context.Context, gameID string, status Status, cfg game.Config) error

	// AppendEvent atomically increments games.event_seq and inserts a new
	// row into game_events, returning the assigned seq. Used by the
	// EventPublisher before a NOTIFY wake-up. Concurrent inserts for the
	// same gameID are serialized by the row-level lock on games.id.
	AppendEvent(ctx context.Context, gameID, eventType string, payload json.RawMessage) (int, error)

	// LoadEvent returns one row by (gameID, seq). Used by the backplane
	// listener to fetch the payload after a NOTIFY wake-up indicates a
	// new seq is available.
	LoadEvent(ctx context.Context, gameID string, seq int) (EventRow, error)

	// EventsSince returns events with seq > sinceSeq, ascending, capped at
	// limit. Backs the HTTP catch-up endpoint clients use to fill any gap
	// when a WebSocket reconnects.
	EventsSince(ctx context.Context, gameID string, sinceSeq, limit int) ([]EventRow, error)

	// CurrentEventSeq returns the latest event_seq value for gameID, or
	// 0 if the game exists but has no events yet. Used at WS open time
	// to tag the initial state snapshot with a sequence number the client
	// can use to detect catch-up gaps on reconnect.
	CurrentEventSeq(ctx context.Context, gameID string) (int, error)

	// EnqueueMatchmake inserts or refreshes a ticket for userID. PK
	// conflict on user_id triggers an upsert that bumps enqueued_at to
	// now, so clicking "find match" twice doesn't stack duplicates and
	// pushes the user to the back of the queue.
	EnqueueMatchmake(ctx context.Context, userID string, players int, mode string, rating int) error

	// CancelMatchmake removes the ticket for userID. No-op when no row
	// exists, so cancelling twice or after a successful match is safe.
	CancelMatchmake(ctx context.Context, userID string) error

	// MatchmakeTick runs one matcher iteration for (players, mode) inside
	// a single transaction. The matcher SELECTs and locks pending rows
	// (FOR UPDATE SKIP LOCKED), hands the locked rows to pairFn for in-Go
	// pairing, then for each returned group creates a new game in
	// `playing` status, seats the players, and deletes their queue rows.
	// All-or-nothing per tick: a transient error rolls everything back and
	// the next tick re-picks the same rows.
	//
	// pairFn receives the locked rows (ordered by enqueued_at ASC, oldest
	// first) and returns groups of exactly `players` users to commit as
	// games. Returning an empty list is fine — the tick commits and waits
	// for the next one. Anyone left in queue stays locked-and-released for
	// the next tick to retry.
	MatchmakeTick(ctx context.Context, players int, mode string, pairFn func([]QueuedUser) [][]QueuedUser) ([]MatchedSeat, error)
}

// EventRow is a persisted row in game_events. Payload stays as
// json.RawMessage so callers can either pass it through to a WS client
// unchanged or unmarshal it into a domain type as needed.
type EventRow struct {
	Seq     int             `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// QueuedUser is one row pulled from matchmake_queue while the matcher
// is deciding pairings. DisplayName is joined from profiles so the
// matcher doesn't need a second round-trip per user.
type QueuedUser struct {
	UserID      string
	Rating      int
	EnqueuedAt  time.Time
	DisplayName string
}

// MatchedSeat is the matcher's output for one user that landed in a
// freshly-created game. The publisher uses GameID to NOTIFY the lobby
// channel and Token to let the client authenticate the seat on its
// first request without a separate join call. Name is the display
// name the matcher wrote into the seats row — surfaced in the
// match_found event so the client can show "You're playing as <name>"
// before the full game state has loaded.
type MatchedSeat struct {
	UserID    string
	GameID    string
	SeatIndex int
	Token     string
	Name      string
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

// UserStats are aggregate counts derived from the user's finished games,
// plus the current Elo for each rating mode. Either rating defaults to
// elo.DefaultRating (1200) when the user has no row for that mode yet.
type UserStats struct {
	Total          int `json:"total"`
	Won            int `json:"won"`
	Lost           int `json:"lost"`
	Ongoing        int `json:"ongoing"`
	RatingOneVOne  int `json:"ratingOneVOne"`
	RatingMulti    int `json:"ratingMulti"`
}

// RatingMode identifies which queue a rating belongs to. chess.com would
// call this "Bullet/Blitz/Rapide"; for Gemline it's the player count split
// — 1v1 and multi each have their own Elo so a strong 1v1 player isn't
// disadvantaged by their inexperience in 4P games (and vice versa).
const (
	RatingMode1v1   = "1v1"
	RatingModeMulti = "multi"
)

// Rating is one user's current Elo + per-result aggregate counts, scoped
// to a single rating mode.
type Rating struct {
	UserID    string
	Mode      string
	Rating    int
	Games     int
	Wins      int
	Losses    int
	Draws     int
	UpdatedAt string // RFC 3339; empty when no row exists yet
}

// RatingUpdate is what ApplyRatedGame persists per user. Result drives the
// wins/losses/draws counter columns; NewRating overrides the rating column.
type RatingUpdate struct {
	UserID    string
	NewRating int
	Result    rune // 'W' | 'L' | 'D'
}

// LeaderboardEntry surfaces a single ranked player on the public board.
// DisplayName is the user's chosen handle (from the profiles table).
type LeaderboardEntry struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Rating      int    `json:"rating"`
	Games       int    `json:"games"`
	Wins        int    `json:"wins"`
	Losses      int    `json:"losses"`
	Draws       int    `json:"draws"`
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
func (noopRepo) RatingFor(context.Context, string, string) (Rating, error) {
	return Rating{}, nil
}
func (noopRepo) RatingsFor(_ context.Context, userIDs []string, mode string) ([]Rating, error) {
	out := make([]Rating, len(userIDs))
	for i, id := range userIDs {
		out[i].UserID = id
		out[i].Mode = mode
	}
	return out, nil
}
func (noopRepo) ApplyRatedGame(context.Context, string, string, []RatingUpdate) (bool, error) {
	// Without DB-backed atomicity we can't guarantee single-application.
	// Return false ("already rated") so Store.maybeApplyRating doesn't
	// double-credit Elo across hermetic tests.
	return false, nil
}
func (noopRepo) Leaderboard(context.Context, string, int) ([]LeaderboardEntry, error) {
	return nil, nil
}
func (noopRepo) FinalizeStart(context.Context, string, Status, game.Config) error { return nil }

// Without a DB the event log doesn't exist; AppendEvent reports 0 as
// the seq and the EventPublisher will refuse to wake up the bus.
// Tests and dev runs without DATABASE_URL fall through to the
// in-process Hub.Deliver path instead.
func (noopRepo) AppendEvent(context.Context, string, string, json.RawMessage) (int, error) {
	return 0, nil
}
func (noopRepo) LoadEvent(context.Context, string, int) (EventRow, error) { return EventRow{}, nil }
func (noopRepo) EventsSince(context.Context, string, int, int) ([]EventRow, error) {
	return nil, nil
}
func (noopRepo) CurrentEventSeq(context.Context, string) (int, error) { return 0, nil }

// Matchmake queue isn't simulated in noop mode — single-process dev runs
// without DATABASE_URL fall back to the legacy synchronous matchmake
// path (still wired in for compatibility), and tests that need queue
// behaviour go through a Postgres-backed Store.
func (noopRepo) EnqueueMatchmake(context.Context, string, int, string, int) error { return nil }
func (noopRepo) CancelMatchmake(context.Context, string) error                    { return nil }
func (noopRepo) MatchmakeTick(context.Context, int, string, func([]QueuedUser) [][]QueuedUser) ([]MatchedSeat, error) {
	return nil, nil
}
