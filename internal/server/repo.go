package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/alexis/gemline/internal/game"
)

// Repository persists game and user state (PostgresRepo, or a test no-op).
// Moves are event-sourced: only the move log is stored and GameState is rebuilt
// by replaying through ApplyMove.
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

	// EnsureProfile inserts a profile row only if absent, never overwriting a
	// chosen name. Ensures every rated user has a row for the leaderboard JOIN.
	EnsureProfile(ctx context.Context, userID, fallbackName string) error

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

	// LobbyGames returns public waiting games, most recent first, capped at
	// limit. Lobby metadata only — no seat tokens, no chat.
	LobbyGames(ctx context.Context, limit int) ([]LobbyEntry, error)

	// SetRematchLink writes rematch_game_id only if unset, and returns the
	// effective ID (newID on success, the existing one on a lost race). Source
	// of truth for resolving concurrent rematch calls.
	SetRematchLink(ctx context.Context, originalID, newID string) (string, error)

	// RatingFor returns the rating row for (userID, mode), or a zero-value
	// Rating when no row exists yet. Callers must apply elo.DefaultRating
	// themselves when Rating.Games == 0.
	RatingFor(ctx context.Context, userID, mode string) (Rating, error)

	// RatingsFor returns rating rows for `userIDs` in the given mode, same
	// order; missing rows are returned as zero-value Rating entries.
	RatingsFor(ctx context.Context, userIDs []string, mode string) ([]Rating, error)

	// ApplyRatedGame atomically marks gameID rated (flipping rated_at NULL→NOW)
	// and persists the rating rows in one transaction. Returns true if this
	// call won the race, false if already rated.
	ApplyRatedGame(ctx context.Context, gameID, mode string, updates []RatingUpdate) (bool, error)

	// RatingsForGame builds the per-seat rating snapshot, including applied
	// deltas once ApplyRatedGame has run. Rated=false for ineligible games
	// (private, or any bot/anonymous seat).
	RatingsForGame(ctx context.Context, gameID string) (GameRatings, error)

	// Leaderboard returns the top-limit rated users for mode (rating DESC).
	// Users with no profile row are omitted.
	Leaderboard(ctx context.Context, mode string, limit int) ([]LeaderboardEntry, error)

	// FinalizeStart, in one transaction, deletes unoccupied seats, updates the
	// thresholds to match the seated player count (rules decided at start, not
	// create), and flips status.
	FinalizeStart(ctx context.Context, gameID string, status Status, cfg game.Config) error

	// DeleteStaleWaitingGames removes waiting games older than olderThan
	// (CASCADE wipes seats/moves/events). Returns the count removed.
	DeleteStaleWaitingGames(ctx context.Context, olderThan time.Duration) (int64, error)

	// PublicProfile returns the publicly-visible profile (name, both ratings,
	// win/loss counts), or ErrProfileNotFound.
	PublicProfile(ctx context.Context, userID string) (PublicProfileSummary, error)

	// SearchProfiles returns up to limit profiles whose display_name matches
	// prefix (case-insensitive). Empty prefix returns nothing.
	SearchProfiles(ctx context.Context, prefix string, limit int) ([]ProfileSearchEntry, error)

	// AppendEvent atomically bumps games.event_seq and inserts the row,
	// returning the seq. Concurrent inserts for one gameID are serialized by
	// the row-level lock on games.id.
	AppendEvent(ctx context.Context, gameID, eventType string, payload json.RawMessage) (int, error)

	// LoadEvent returns one row by (gameID, seq), fetched after a NOTIFY.
	LoadEvent(ctx context.Context, gameID string, seq int) (EventRow, error)

	// EventsSince returns events with seq > sinceSeq, ascending, capped at
	// limit. Backs the catch-up endpoint on WS reconnect.
	EventsSince(ctx context.Context, gameID string, sinceSeq, limit int) ([]EventRow, error)

	// CurrentEventSeq returns the latest event_seq, or 0 if none yet. Tags the
	// initial WS snapshot so the client can detect catch-up gaps.
	CurrentEventSeq(ctx context.Context, gameID string) (int, error)

	// EnqueueMatchmake upserts a ticket, bumping enqueued_at so a re-click
	// moves the user to the back of the queue rather than stacking duplicates.
	EnqueueMatchmake(ctx context.Context, userID string, players int, mode string, rating int) error

	// CancelMatchmake removes the user's ticket; no-op when absent.
	CancelMatchmake(ctx context.Context, userID string) error

	// MatchmakeTick runs one matcher iteration in a transaction: lock pending
	// rows (FOR UPDATE SKIP LOCKED), pass them to pairFn, then create a game +
	// seats + delete queue rows per group. All-or-nothing per tick.
	//
	// pairFn gets the locked rows (enqueued_at ASC) and returns groups to
	// commit; an empty list commits and waits for the next tick.
	MatchmakeTick(ctx context.Context, players int, mode string, pairFn func([]QueuedUser) [][]QueuedUser) ([]MatchedSeat, error)

	// MatchmakeQueueSnapshot returns the current queue (enqueued_at ASC)
	// without locking, for queue_update notifications after each tick.
	MatchmakeQueueSnapshot(ctx context.Context, players int, mode string) ([]QueuedUser, error)

	// SaveRematchOffer writes (JSON body) or clears (nil) rematch_offer. Must
	// be called on every rec.RematchOffer mutation so other pods see it on
	// reload after invalidation.
	SaveRematchOffer(ctx context.Context, gameID string, offer []byte) error

	// SaveDrawOffer writes draw_offer_by (offering seat index, or -1 to clear).
	// Must follow every rec.DrawOfferBy mutation: the opponent's /draw/accept
	// may land on a pod whose cache was invalidated, and its reload must see
	// the offer.
	SaveDrawOffer(ctx context.Context, gameID string, offerBy int) error
}

// EventRow is a persisted game_events row. Payload stays raw so callers can
// pass it straight to a WS client or unmarshal as needed.
type EventRow struct {
	Seq     int             `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// QueuedUser is one matchmake_queue row; DisplayName is joined from profiles
// to save a round-trip per user.
type QueuedUser struct {
	UserID      string
	Rating      int
	EnqueuedAt  time.Time
	DisplayName string
}

// MatchedSeat is the matcher's output for one matched user. Token lets the
// client authenticate the seat without a separate join; Name is surfaced in the
// match_found event before full state loads.
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
	GameID      string     `json:"gameId"`
	Status      Status     `json:"status"`
	SeatIndex   int        `json:"seatIndex"`
	Color       game.Color `json:"color"`
	WinnerColor game.Color `json:"winnerColor"`
	Outcome     string     `json:"outcome"` // "won", "lost", "ongoing"
	MoveCount   int        `json:"moveCount"`
	CreatedAt   string     `json:"createdAt"`
	UpdatedAt   string     `json:"updatedAt"`
}

// UserStats are aggregate counts derived from the user's finished games,
// plus the current Elo for each rating mode. Either rating defaults to
// elo.DefaultRating (1200) when the user has no row for that mode yet.
type UserStats struct {
	Total         int `json:"total"`
	Won           int `json:"won"`
	Lost          int `json:"lost"`
	Ongoing       int `json:"ongoing"`
	RatingOneVOne int `json:"ratingOneVOne"`
	RatingMulti   int `json:"ratingMulti"`
}

// RatingMode splits ratings by player count: 1v1 and multi keep separate Elos
// so skill in one doesn't penalise the other.
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

// PublicProfileSummary is the publicly-visible payload for GET /api/users/:id.
type PublicProfileSummary struct {
	UserID        string `json:"userId"`
	DisplayName   string `json:"displayName"`
	RatingOneVOne int    `json:"ratingOneVOne"`
	RatingMulti   int    `json:"ratingMulti"`
	GamesOneVOne  int    `json:"gamesOneVOne"`
	GamesMulti    int    `json:"gamesMulti"`
	Won           int    `json:"won"`
	Lost          int    `json:"lost"`
	Draws         int    `json:"draws"`
}

// ProfileSearchEntry is one "invite a friend" search result.
type ProfileSearchEntry struct {
	UserID        string `json:"userId"`
	DisplayName   string `json:"displayName"`
	RatingOneVOne int    `json:"ratingOneVOne"`
}

// ErrProfileNotFound is translated to a 404 by the handler.
var ErrProfileNotFound = errors.New("profile not found")

// RatingUpdate is what ApplyRatedGame persists per user. OldRating is recorded
// in rating_history alongside the delta so the UI can show "+12 / -8" without
// client-side subtraction.
type RatingUpdate struct {
	UserID    string
	OldRating int
	NewRating int
	Result    rune // 'W' | 'L' | 'D'
}

// SeatRating is the per-seat rating snapshot in the "rated" WS event. The
// applied-only fields are set only once ApplyRatedGame has run.
type SeatRating struct {
	SeatIndex     int    `json:"seatIndex"`
	UserID        string `json:"userId"`
	CurrentRating int    `json:"currentRating"`
	// Applied-only; omitempty keeps the unrated wire shape small.
	OldRating int    `json:"oldRating,omitempty"`
	NewRating int    `json:"newRating,omitempty"`
	Delta     int    `json:"delta,omitempty"`
	Result    string `json:"result,omitempty"` // "W" | "L" | "D"
}

// GameRatings is the ratings payload (HTTP + "rated" WS event). Rated means
// eligible (public, no bots, all authed); Applied means the Elo math has run.
type GameRatings struct {
	Mode    string       `json:"mode"`
	Rated   bool         `json:"rated"`
	Applied bool         `json:"applied"`
	Seats   []SeatRating `json:"seats"`
}

// LeaderboardEntry is one ranked player on the public board.
type LeaderboardEntry struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Rating      int    `json:"rating"`
	Games       int    `json:"games"`
	Wins        int    `json:"wins"`
	Losses      int    `json:"losses"`
	Draws       int    `json:"draws"`
}

// Message is a chat line; AuthorColor/AuthorName are snapshots from post time.
type Message struct {
	ID          int64      `json:"id"`
	GameID      string     `json:"gameId"`
	SeatIndex   int        `json:"seatIndex"`
	AuthorColor game.Color `json:"authorColor"`
	AuthorName  string     `json:"authorName"`
	Body        string     `json:"body"`
	SentAt      string     `json:"sentAt"`
}

// noopRepo lets the in-memory Store run without a database; loads return
// (nil, nil) so callers fall back to whatever they hold in memory.
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
func (noopRepo) Profile(context.Context, string) (*Profile, error)   { return nil, nil }
func (noopRepo) UpsertProfile(context.Context, string, string) error { return nil }
func (noopRepo) EnsureProfile(context.Context, string, string) error { return nil }
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
	// Return newID to keep Store.Rematch's idempotency contract for in-memory runs.
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
	// Return false ("already rated") so maybeApplyRating can't double-credit
	// without DB-backed atomicity.
	return false, nil
}
func (noopRepo) Leaderboard(context.Context, string, int) ([]LeaderboardEntry, error) {
	return nil, nil
}

// Report Rated=false in noop mode so the client hides the Elo section.
func (noopRepo) RatingsForGame(context.Context, string) (GameRatings, error) {
	return GameRatings{Rated: false, Applied: false, Seats: []SeatRating{}}, nil
}
func (noopRepo) FinalizeStart(context.Context, string, Status, game.Config) error { return nil }
func (noopRepo) DeleteStaleWaitingGames(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

// Return ErrProfileNotFound so the handler's 404 path stays exercised in tests.
func (noopRepo) PublicProfile(context.Context, string) (PublicProfileSummary, error) {
	return PublicProfileSummary{}, ErrProfileNotFound
}
func (noopRepo) SearchProfiles(context.Context, string, int) ([]ProfileSearchEntry, error) {
	return []ProfileSearchEntry{}, nil
}

// With no event log, AppendEvent returns seq 0 so the EventPublisher won't wake
// the bus; runs without DATABASE_URL use the in-process Hub.Deliver path.
func (noopRepo) AppendEvent(context.Context, string, string, json.RawMessage) (int, error) {
	return 0, nil
}
func (noopRepo) LoadEvent(context.Context, string, int) (EventRow, error) { return EventRow{}, nil }
func (noopRepo) EventsSince(context.Context, string, int, int) ([]EventRow, error) {
	return nil, nil
}
func (noopRepo) CurrentEventSeq(context.Context, string) (int, error) { return 0, nil }

// The matchmake queue isn't simulated in noop mode; queue tests use a
// Postgres-backed Store, and dev runs fall back to the synchronous path.
func (noopRepo) EnqueueMatchmake(context.Context, string, int, string, int) error { return nil }
func (noopRepo) CancelMatchmake(context.Context, string) error                    { return nil }
func (noopRepo) MatchmakeTick(context.Context, int, string, func([]QueuedUser) [][]QueuedUser) ([]MatchedSeat, error) {
	return nil, nil
}
func (noopRepo) MatchmakeQueueSnapshot(context.Context, int, string) ([]QueuedUser, error) {
	return nil, nil
}
func (noopRepo) SaveRematchOffer(context.Context, string, []byte) error { return nil }
func (noopRepo) SaveDrawOffer(context.Context, string, int) error       { return nil }
