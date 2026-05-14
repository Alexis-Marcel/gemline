package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alexis/gemline/internal/game"
)

// PostgresRepo persists games to a Postgres database. Schema is in
// internal/db/migrations.
type PostgresRepo struct {
	pool *sql.DB
}

func NewPostgresRepo(pool *sql.DB) *PostgresRepo {
	return &PostgresRepo{pool: pool}
}

func (r *PostgresRepo) SaveNewGame(ctx context.Context, rec *GameRecord) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cfg := rec.State.Config
	vis := rec.Visibility
	if vis == "" {
		vis = VisibilityPrivate
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO games (id, status, board_side, capture_pairs_win, align4_to_win, align5_to_win, initial_time_ms, increment_ms, visibility)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, rec.ID, rec.Status, cfg.BoardSide, cfg.CapturePairsWin, cfg.Align4ToWin, cfg.Align5ToWin, cfg.InitialTimeMs, cfg.IncrementMs, string(vis))
	if err != nil {
		return fmt.Errorf("insert game: %w", err)
	}

	for _, s := range rec.Seats {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO seats (game_id, seat_index, color, name, token_hash, user_id, occupied, is_bot)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, rec.ID, s.Index, int(s.Color), s.Name, nilIfEmpty(s.TokenHash), nilIfEmptyStr(s.UserID), s.Occupied, s.IsBot)
		if err != nil {
			return fmt.Errorf("insert seat %d: %w", s.Index, err)
		}
	}
	return tx.Commit()
}

func (r *PostgresRepo) LoadGame(ctx context.Context, id string) (*GameRecord, error) {
	row := r.pool.QueryRowContext(ctx, `
		SELECT status, board_side, capture_pairs_win, align4_to_win, align5_to_win,
		       winner_color, win_kind, initial_time_ms, increment_ms, created_at,
		       visibility, rematch_game_id
		FROM games WHERE id = $1
	`, id)
	var (
		status        Status
		boardSide     int
		captureWin    int
		align4        int
		align5        int
		winnerColor   int
		winKind       int
		initialTimeMs int64
		incrementMs   int64
		createdAt     time.Time
		visibility    string
		rematchID     sql.NullString
	)
	if err := row.Scan(&status, &boardSide, &captureWin, &align4, &align5, &winnerColor, &winKind, &initialTimeMs, &incrementMs, &createdAt, &visibility, &rematchID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("select game: %w", err)
	}

	// Seats — needed to know the player roster before we can construct GameState.
	seatRows, err := r.pool.QueryContext(ctx, `
		SELECT seat_index, color, name, token_hash, user_id, occupied, is_bot
		FROM seats WHERE game_id = $1 ORDER BY seat_index
	`, id)
	if err != nil {
		return nil, fmt.Errorf("select seats: %w", err)
	}
	defer seatRows.Close()

	var seats []Seat
	var colors []game.Color
	for seatRows.Next() {
		var (
			idx       int
			colorInt  int
			name      string
			tokenHash []byte
			userID    sql.NullString
			occupied  bool
			isBot     bool
		)
		if err := seatRows.Scan(&idx, &colorInt, &name, &tokenHash, &userID, &occupied, &isBot); err != nil {
			return nil, fmt.Errorf("scan seat: %w", err)
		}
		seats = append(seats, Seat{
			Index:     idx,
			Color:     game.Color(colorInt),
			Name:      name,
			TokenHash: tokenHash,
			UserID:    userID.String,
			Occupied:  occupied,
			IsBot:     isBot,
		})
		colors = append(colors, game.Color(colorInt))
	}
	if err := seatRows.Err(); err != nil {
		return nil, err
	}

	cfg := game.Config{
		BoardSide:       boardSide,
		CapturePairsWin: captureWin,
		Align4ToWin:     align4,
		Align5ToWin:     align5,
		InitialTimeMs:   initialTimeMs,
		IncrementMs:     incrementMs,
	}
	state := game.NewGame(colors, cfg)

	// Replay moves through ApplyMove so captures, wins, AND chess-clock
	// deductions are reproduced from the same rule engine that produced
	// them at play time. played_at is passed as the move's "now" so each
	// player's TimeRemainingMs converges to the same value it had live.
	//
	// We deliberately do NOT pre-seed state.Winner from the DB here:
	// ApplyMove rejects further moves once IsOver() is true, so for any
	// finished game the very first replay call would return ErrGameOver
	// and LoadGame would fail. The persisted Winner/WinKind are
	// re-applied after the loop, which:
	//   - handles move-driven endings (alignment, capture) naturally
	//     since the engine sets Winner during the last move's apply;
	//   - handles externally-driven endings (resign, timeout, draw)
	//     by overlaying the DB's recorded outcome on top of the
	//     replayed board state.
	moveRows, err := r.pool.QueryContext(ctx, `
		SELECT color, q, r, played_at FROM moves WHERE game_id = $1 ORDER BY ordinal
	`, id)
	if err != nil {
		return nil, fmt.Errorf("select moves: %w", err)
	}
	defer moveRows.Close()

	// Walk the move log to rebuild the in-memory state. We start the clock
	// from a reference that won't accidentally drain time for a game whose
	// players haven't been around: use the first move's played_at if any,
	// otherwise leave TurnStartedAt zero until the live code path sets it.
	var lastPlayed time.Time
	for moveRows.Next() {
		var (
			colorInt int
			q, rr    int
			playedAt time.Time
		)
		if err := moveRows.Scan(&colorInt, &q, &rr, &playedAt); err != nil {
			return nil, fmt.Errorf("scan move: %w", err)
		}
		if state.TurnStartedAt.IsZero() {
			state.TurnStartedAt = playedAt // first move's start = its own timestamp
		}
		if _, err := state.ApplyMove(game.Move{
			Player: game.Color(colorInt),
			Pos:    game.Position{Q: q, R: rr},
		}, playedAt); err != nil {
			return nil, fmt.Errorf("replay move: %w", err)
		}
		lastPlayed = playedAt
	}
	if err := moveRows.Err(); err != nil {
		return nil, err
	}

	// On reload, if the game is in progress with no moves yet, restart the
	// clock from "now" rather than created_at — otherwise a long-idle game
	// would flag the active player immediately on load.
	if status == StatusPlaying && lastPlayed.IsZero() && state.ClockEnabled() {
		state.TurnStartedAt = time.Now()
	}

	// Trust the DB for the outcome: replay handles move-driven wins,
	// but resign / timeout / draw leave the board state empty of an
	// engine-set Winner and need this overlay. Idempotent for
	// move-driven endings — the engine has already set the same
	// values during the last ApplyMove.
	state.Winner = game.Color(winnerColor)
	state.WinKind = game.WinKind(winKind)

	return &GameRecord{
		ID:            id,
		State:         state,
		Seats:         seats,
		Status:        status,
		Visibility:    Visibility(visibility),
		RematchGameID: rematchID.String,
		CreatedAt:     createdAt,
		DrawOfferBy:   -1, // draw offers don't survive restarts; players can re-offer
	}, nil
}

func (r *PostgresRepo) UpdateSeat(ctx context.Context, gameID string, seat *Seat, status Status) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		UPDATE seats SET name = $1, token_hash = $2, user_id = $3, occupied = $4, is_bot = $5
		WHERE game_id = $6 AND seat_index = $7
	`, seat.Name, seat.TokenHash, nilIfEmptyStr(seat.UserID), seat.Occupied, seat.IsBot, gameID, seat.Index)
	if err != nil {
		return fmt.Errorf("update seat: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE games SET status = $1, updated_at = NOW() WHERE id = $2
	`, status, gameID)
	if err != nil {
		return fmt.Errorf("update game status: %w", err)
	}
	return tx.Commit()
}

func (r *PostgresRepo) UpdateOutcome(ctx context.Context, gameID string, status Status, winner game.Color, winKind game.WinKind) error {
	_, err := r.pool.ExecContext(ctx, `
		UPDATE games
		SET status = $1, winner_color = $2, win_kind = $3, updated_at = NOW()
		WHERE id = $4
	`, status, int(winner), int(winKind), gameID)
	return err
}

func (r *PostgresRepo) AppendMove(ctx context.Context, gameID string, ordinal int, m game.Move, winner game.Color, winKind game.WinKind, status Status) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO moves (game_id, ordinal, color, q, r)
		VALUES ($1, $2, $3, $4, $5)
	`, gameID, ordinal, int(m.Player), m.Pos.Q, m.Pos.R)
	if err != nil {
		return fmt.Errorf("insert move: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE games
		SET status = $1, winner_color = $2, win_kind = $3, updated_at = NOW()
		WHERE id = $4
	`, status, int(winner), int(winKind), gameID)
	if err != nil {
		return fmt.Errorf("update game: %w", err)
	}
	return tx.Commit()
}

// nilIfEmpty returns nil for an empty byte slice so it is stored as SQL NULL
// rather than as a zero-length bytea (which would compare differently).
func nilIfEmpty(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nilIfEmptyStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (r *PostgresRepo) Profile(ctx context.Context, userID string) (*Profile, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT display_name FROM profiles WHERE user_id = $1`, userID)
	var name string
	if err := row.Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &Profile{UserID: userID, DisplayName: name}, nil
}

func (r *PostgresRepo) UpsertProfile(ctx context.Context, userID, displayName string) error {
	_, err := r.pool.ExecContext(ctx, `
		INSERT INTO profiles (user_id, display_name) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET display_name = EXCLUDED.display_name, updated_at = NOW()
	`, userID, displayName)
	return err
}

// EnsureProfile is the create-only sibling of UpsertProfile: it never
// overwrites an existing display_name. Useful at rating-apply time and
// in /api/auth/me, where we want every authenticated player to have a
// profile row without stomping on a name they may have set themselves.
func (r *PostgresRepo) EnsureProfile(ctx context.Context, userID, fallbackName string) error {
	_, err := r.pool.ExecContext(ctx, `
		INSERT INTO profiles (user_id, display_name) VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING
	`, userID, fallbackName)
	return err
}

func (r *PostgresRepo) GamesForUser(ctx context.Context, userID string, limit int) ([]UserGame, error) {
	// Only finished games surface in the history view. Waiting/playing
	// games are either still in progress (the user has them open in
	// another tab) or abandoned — neither belongs on a "ce que tu as
	// joué" list. Stale waiting games are reaped separately by the
	// stale-game cleaner (see Store.StartStaleGameCleaner).
	rows, err := r.pool.QueryContext(ctx, `
		SELECT g.id, g.status, s.seat_index, s.color, g.winner_color,
		       (SELECT COUNT(*) FROM moves m WHERE m.game_id = g.id),
		       g.created_at, g.updated_at
		FROM seats s
		JOIN games g ON g.id = s.game_id
		WHERE s.user_id = $1 AND g.status = 'finished'
		ORDER BY g.updated_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UserGame, 0)
	for rows.Next() {
		var (
			ug         UserGame
			color      int
			winner     int
			createdAt  time.Time
			updatedAt  time.Time
		)
		if err := rows.Scan(&ug.GameID, &ug.Status, &ug.SeatIndex, &color, &winner, &ug.MoveCount, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		ug.Color = game.Color(color)
		ug.WinnerColor = game.Color(winner)
		ug.Outcome = deriveOutcome(ug.Status, ug.Color, ug.WinnerColor)
		ug.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		ug.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, ug)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) StatsForUser(ctx context.Context, userID string) (UserStats, error) {
	// Per-mode ratings are pulled via correlated subqueries with the
	// elo.DefaultRating fallback baked in. Keeping it in a single query
	// avoids a round-trip per mode at the cost of slightly busier SQL.
	row := r.pool.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) AS total,
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color = s.color) AS won,
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color <> s.color AND g.winner_color <> 0) AS lost,
		  COUNT(*) FILTER (WHERE g.status <> 'finished') AS ongoing,
		  COALESCE((SELECT rating FROM ratings WHERE user_id = $1 AND mode = '1v1'), 1200) AS rating_1v1,
		  COALESCE((SELECT rating FROM ratings WHERE user_id = $1 AND mode = 'multi'), 1200) AS rating_multi
		FROM seats s
		JOIN games g ON g.id = s.game_id
		WHERE s.user_id = $1
	`, userID)
	var st UserStats
	if err := row.Scan(&st.Total, &st.Won, &st.Lost, &st.Ongoing, &st.RatingOneVOne, &st.RatingMulti); err != nil {
		return UserStats{}, err
	}
	return st, nil
}

func (r *PostgresRepo) AppendMessage(ctx context.Context, m *Message) error {
	var sentAt time.Time
	if err := r.pool.QueryRowContext(ctx, `
		INSERT INTO messages (game_id, seat_index, author_color, author_name, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, sent_at
	`, m.GameID, m.SeatIndex, int(m.AuthorColor), m.AuthorName, m.Body).Scan(&m.ID, &sentAt); err != nil {
		return err
	}
	m.SentAt = sentAt.UTC().Format(time.RFC3339Nano)
	return nil
}

func (r *PostgresRepo) MessagesForGame(ctx context.Context, gameID string, limit int) ([]Message, error) {
	// Fetch the most-recent `limit` rows then reverse so the response is in
	// chronological order (oldest first) — the natural order for rendering.
	rows, err := r.pool.QueryContext(ctx, `
		SELECT id, seat_index, author_color, author_name, body, sent_at
		FROM messages WHERE game_id = $1
		ORDER BY sent_at DESC LIMIT $2
	`, gameID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Message, 0)
	for rows.Next() {
		var (
			m        Message
			color    int
			sentAt   time.Time
		)
		if err := rows.Scan(&m.ID, &m.SeatIndex, &color, &m.AuthorName, &m.Body, &sentAt); err != nil {
			return nil, err
		}
		m.GameID = gameID
		m.AuthorColor = game.Color(color)
		m.SentAt = sentAt.UTC().Format(time.RFC3339Nano)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse in-place: oldest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (r *PostgresRepo) LobbyGames(ctx context.Context, limit int) ([]LobbyEntry, error) {
	// Public + still-waiting games, with a single denormalised seat-occupancy
	// count so the lobby UI can show "1/2" without us shipping the full seat
	// list. The supporting partial index is games_public_waiting_idx.
	rows, err := r.pool.QueryContext(ctx, `
		SELECT g.id,
		       (SELECT COUNT(*) FROM seats s WHERE s.game_id = g.id) AS players,
		       (SELECT COUNT(*) FROM seats s WHERE s.game_id = g.id AND s.occupied) AS seated,
		       g.created_at
		FROM games g
		WHERE g.status = 'waiting' AND g.visibility = 'public'
		ORDER BY g.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]LobbyEntry, 0)
	for rows.Next() {
		var e LobbyEntry
		if err := rows.Scan(&e.GameID, &e.Players, &e.Seated, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) SetRematchLink(ctx context.Context, originalID, newID string) (string, error) {
	// COALESCE preserves an already-set link, so the second writer in a race
	// is a no-op and we always observe the winning ID with a single RETURNING
	// — no extra SELECT, no row-level lock dance.
	row := r.pool.QueryRowContext(ctx, `
		UPDATE games
		SET rematch_game_id = COALESCE(rematch_game_id, $1)
		WHERE id = $2
		RETURNING rematch_game_id
	`, newID, originalID)
	var got sql.NullString
	if err := row.Scan(&got); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrGameNotFound
		}
		return "", err
	}
	if !got.Valid || got.String == "" {
		// Shouldn't happen — COALESCE($1, …) makes the result non-null on
		// any successful UPDATE — but tolerate it rather than crashing.
		return newID, nil
	}
	return got.String, nil
}

func (r *PostgresRepo) RatingFor(ctx context.Context, userID, mode string) (Rating, error) {
	row := r.pool.QueryRowContext(ctx, `
		SELECT rating, games, wins, losses, draws, updated_at
		FROM ratings WHERE user_id = $1 AND mode = $2
	`, userID, mode)
	var (
		rt        Rating
		updatedAt time.Time
	)
	rt.UserID = userID
	rt.Mode = mode
	if err := row.Scan(&rt.Rating, &rt.Games, &rt.Wins, &rt.Losses, &rt.Draws, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Rating{UserID: userID, Mode: mode}, nil
		}
		return Rating{}, err
	}
	rt.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return rt, nil
}

func (r *PostgresRepo) RatingsFor(ctx context.Context, userIDs []string, mode string) ([]Rating, error) {
	out := make([]Rating, len(userIDs))
	for i, id := range userIDs {
		rt, err := r.RatingFor(ctx, id, mode)
		if err != nil {
			return nil, err
		}
		out[i] = rt
	}
	return out, nil
}

func (r *PostgresRepo) ApplyRatedGame(ctx context.Context, gameID, mode string, updates []RatingUpdate) (bool, error) {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Single-writer guard: only the goroutine that flips rated_at NULL→NOW
	// actually applies the rating math. A subsequent caller sees no row
	// returned and bails out without double-crediting Elo.
	var marked sql.NullString
	if err := tx.QueryRowContext(ctx, `
		UPDATE games SET rated_at = NOW()
		WHERE id = $1 AND rated_at IS NULL
		RETURNING id
	`, gameID).Scan(&marked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // already rated by someone else
		}
		return false, fmt.Errorf("mark rated: %w", err)
	}

	for _, u := range updates {
		// New columns are computed in the UPDATE so missing rows (first
		// rated game ever) start from the defaults seeded by INSERT.
		var win, loss, draw int
		switch u.Result {
		case 'W':
			win = 1
		case 'L':
			loss = 1
		case 'D':
			draw = 1
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO ratings (user_id, mode, rating, games, wins, losses, draws)
			VALUES ($1, $2, $3, 1, $4, $5, $6)
			ON CONFLICT (user_id, mode) DO UPDATE
			SET rating     = EXCLUDED.rating,
			    games      = ratings.games + 1,
			    wins       = ratings.wins + EXCLUDED.wins,
			    losses     = ratings.losses + EXCLUDED.losses,
			    draws      = ratings.draws + EXCLUDED.draws,
			    updated_at = NOW()
		`, u.UserID, mode, u.NewRating, win, loss, draw)
		if err != nil {
			return false, fmt.Errorf("upsert rating %s/%s: %w", u.UserID, mode, err)
		}
		// Persist the delta in rating_history so the end-of-game UI
		// can show "+12 / -8" without doing arithmetic on whatever
		// the user's *current* rating happens to be later.
		// PK (game_id, user_id) protects against double inserts; this
		// commits with the ratings UPDATE so they're never out of sync.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rating_history (game_id, user_id, old_rating, new_rating, delta, result)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, gameID, u.UserID, u.OldRating, u.NewRating, u.NewRating-u.OldRating, string(u.Result)); err != nil {
			return false, fmt.Errorf("insert rating_history %s: %w", u.UserID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RatingsForGame builds the per-seat rating snapshot for one game. It
// queries seats + games + ratings + rating_history in one round each
// rather than one per user, because the game ID alone is sufficient
// for every lookup. Returns Rated=false (with empty Seats) for games
// that aren't matchmaking-eligible.
//
// Decision tree:
//   - game.visibility != public OR any seat is_bot OR any seat user_id NULL
//     → Rated=false, no seats body (UI hides the Elo section)
//   - rated_at IS NULL → Rated=true, Applied=false, Seats with
//     CurrentRating only
//   - rated_at IS NOT NULL → Rated=true, Applied=true, Seats with
//     CurrentRating + the historical Old/New/Delta/Result fields
func (r *PostgresRepo) RatingsForGame(ctx context.Context, gameID string) (GameRatings, error) {
	// Step 1: game metadata + ratedness gate
	var (
		visibility string
		ratedAt    sql.NullTime
		seatCount  int
	)
	row := r.pool.QueryRowContext(ctx, `
		SELECT g.visibility, g.rated_at, (SELECT COUNT(*) FROM seats s WHERE s.game_id = g.id)
		FROM games g WHERE g.id = $1
	`, gameID)
	if err := row.Scan(&visibility, &ratedAt, &seatCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GameRatings{}, ErrGameNotFound
		}
		return GameRatings{}, fmt.Errorf("ratings-for-game game lookup: %w", err)
	}

	mode := RatingModeMulti
	if seatCount == 2 {
		mode = RatingMode1v1
	}

	// Step 2: are all seats rateable (no bot, no anon)?
	if visibility != string(VisibilityPublic) {
		return GameRatings{Mode: mode, Rated: false, Applied: false, Seats: []SeatRating{}}, nil
	}
	rateableRow := r.pool.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM seats
		WHERE game_id = $1 AND (is_bot = TRUE OR user_id IS NULL OR occupied = FALSE)
	`, gameID)
	var disqualifiers int
	if err := rateableRow.Scan(&disqualifiers); err != nil {
		return GameRatings{}, fmt.Errorf("ratings-for-game disqualifier scan: %w", err)
	}
	if disqualifiers > 0 {
		return GameRatings{Mode: mode, Rated: false, Applied: false, Seats: []SeatRating{}}, nil
	}

	// Step 3: pull seats + current ratings + history (LEFT JOIN — rows
	// are absent for in-progress games)
	rows, err := r.pool.QueryContext(ctx, `
		SELECT s.seat_index, s.user_id,
		       COALESCE(r.rating, 1200),
		       h.old_rating, h.new_rating, h.delta, h.result
		FROM seats s
		LEFT JOIN ratings r ON r.user_id = s.user_id AND r.mode = $2
		LEFT JOIN rating_history h ON h.user_id = s.user_id AND h.game_id = s.game_id
		WHERE s.game_id = $1
		ORDER BY s.seat_index
	`, gameID, mode)
	if err != nil {
		return GameRatings{}, fmt.Errorf("ratings-for-game seats: %w", err)
	}
	defer rows.Close()

	applied := ratedAt.Valid
	out := GameRatings{Mode: mode, Rated: true, Applied: applied, Seats: []SeatRating{}}
	for rows.Next() {
		var (
			seatIdx      int
			userID       sql.NullString
			currentRtg   int
			oldRtg       sql.NullInt32
			newRtg       sql.NullInt32
			delta        sql.NullInt32
			result       sql.NullString
		)
		if err := rows.Scan(&seatIdx, &userID, &currentRtg, &oldRtg, &newRtg, &delta, &result); err != nil {
			return GameRatings{}, fmt.Errorf("ratings-for-game scan: %w", err)
		}
		sr := SeatRating{
			SeatIndex:     seatIdx,
			UserID:        userID.String,
			CurrentRating: currentRtg,
		}
		if applied && oldRtg.Valid {
			sr.OldRating = int(oldRtg.Int32)
			sr.NewRating = int(newRtg.Int32)
			sr.Delta = int(delta.Int32)
			sr.Result = result.String
		}
		out.Seats = append(out.Seats, sr)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) Leaderboard(ctx context.Context, mode string, limit int) ([]LeaderboardEntry, error) {
	// Inner join on profiles — anonymous-only users with no display name
	// shouldn't appear on a public board. Filtered by mode so a strong 1v1
	// player doesn't end up high on the multi board (and vice versa).
	rows, err := r.pool.QueryContext(ctx, `
		SELECT r.user_id, p.display_name, r.rating, r.games, r.wins, r.losses, r.draws
		FROM ratings r
		JOIN profiles p ON p.user_id = r.user_id
		WHERE r.mode = $1
		ORDER BY r.rating DESC, r.updated_at DESC
		LIMIT $2
	`, mode, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]LeaderboardEntry, 0)
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(&e.UserID, &e.DisplayName, &e.Rating, &e.Games, &e.Wins, &e.Losses, &e.Draws); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) FinalizeStart(ctx context.Context, gameID string, status Status, cfg game.Config) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM seats WHERE game_id = $1 AND occupied = FALSE
	`, gameID); err != nil {
		return fmt.Errorf("trim seats: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE games SET
			status = $1,
			capture_pairs_win = $2,
			align4_to_win = $3,
			align5_to_win = $4,
			updated_at = NOW()
		WHERE id = $5
	`, status, cfg.CapturePairsWin, cfg.Align4ToWin, cfg.Align5ToWin, gameID); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return tx.Commit()
}

// PublicProfile aggregates the publicly-visible bits for a user. All
// numbers are derived from the canonical tables (profiles, ratings,
// games + seats) — no separate stats table. Counts are scoped to
// finished games so a half-played private room doesn't pump the
// numbers. Default rating (1200) is applied via COALESCE when a user
// has no rated row for a given mode yet.
func (r *PostgresRepo) PublicProfile(ctx context.Context, userID string) (PublicProfileSummary, error) {
	// First check the profile row exists; without one the user is
	// effectively a ghost (no display name to render).
	row := r.pool.QueryRowContext(ctx, `SELECT display_name FROM profiles WHERE user_id = $1`, userID)
	var displayName string
	if err := row.Scan(&displayName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublicProfileSummary{}, ErrProfileNotFound
		}
		return PublicProfileSummary{}, fmt.Errorf("profile lookup: %w", err)
	}

	// Aggregate counts in one round-trip. The win/lost/draw filters
	// mirror the existing StatsForUser query — we keep them in sync
	// by hand for now.
	statsRow := r.pool.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color = s.color) AS won,
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color <> s.color AND g.winner_color <> 0) AS lost,
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color = 0) AS draws,
		  COALESCE((SELECT rating FROM ratings WHERE user_id = $1 AND mode = '1v1'), 1200) AS r1v1,
		  COALESCE((SELECT games  FROM ratings WHERE user_id = $1 AND mode = '1v1'), 0)    AS g1v1,
		  COALESCE((SELECT rating FROM ratings WHERE user_id = $1 AND mode = 'multi'), 1200) AS rmulti,
		  COALESCE((SELECT games  FROM ratings WHERE user_id = $1 AND mode = 'multi'), 0)    AS gmulti
		FROM seats s
		JOIN games g ON g.id = s.game_id
		WHERE s.user_id = $1
	`, userID)
	out := PublicProfileSummary{UserID: userID, DisplayName: displayName}
	if err := statsRow.Scan(&out.Won, &out.Lost, &out.Draws, &out.RatingOneVOne, &out.GamesOneVOne, &out.RatingMulti, &out.GamesMulti); err != nil {
		return PublicProfileSummary{}, fmt.Errorf("profile stats: %w", err)
	}
	return out, nil
}

// SearchProfiles returns up to `limit` profile rows whose display
// name starts with `prefix` (case-insensitive). The join on ratings
// brings the 1v1 elo along so the picker shows "Alice (1450)" rather
// than a bare name — helps disambiguate two Alices.
//
// Empty prefix returns nothing on purpose: callers shouldn't be able
// to scrape the whole user table by sending q=. limit is clamped at
// 50 for the same reason.
func (r *PostgresRepo) SearchProfiles(ctx context.Context, prefix string, limit int) ([]ProfileSearchEntry, error) {
	if prefix == "" {
		return []ProfileSearchEntry{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := r.pool.QueryContext(ctx, `
		SELECT p.user_id, p.display_name,
		       COALESCE(r.rating, 1200) AS r1v1
		FROM profiles p
		LEFT JOIN ratings r ON r.user_id = p.user_id AND r.mode = '1v1'
		WHERE p.display_name ILIKE $1 || '%'
		ORDER BY p.display_name
		LIMIT $2
	`, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search profiles: %w", err)
	}
	defer rows.Close()
	out := make([]ProfileSearchEntry, 0)
	for rows.Next() {
		var e ProfileSearchEntry
		if err := rows.Scan(&e.UserID, &e.DisplayName, &e.RatingOneVOne); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteStaleWaitingGames removes games stuck in 'waiting' for longer
// than olderThan. Uses updated_at as the freshness signal — joining /
// adding bots / removing bots all bump updated_at, so an active host
// who's still adjusting their lobby today won't be reaped by a 7d
// threshold even if they created the game last week. ON DELETE
// CASCADE handles seats + game_events + rating_history (the latter
// shouldn't exist for waiting games anyway, but cheap insurance).
func (r *PostgresRepo) DeleteStaleWaitingGames(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	res, err := r.pool.ExecContext(ctx, `
		DELETE FROM games WHERE status = 'waiting' AND updated_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete stale waiting games: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func deriveOutcome(status Status, mine, winner game.Color) string {
	if status != StatusFinished {
		return "ongoing"
	}
	if winner == game.Empty {
		return "draw"
	}
	if winner == mine {
		return "won"
	}
	return "lost"
}

// AppendEvent inserts one row into game_events with a monotonically
// increasing per-game seq. The CTE bumps games.event_seq and feeds the
// new value to the INSERT in one statement — the row-level lock on
// games.id serializes concurrent writers without explicit locking.
//
// If gameID does not exist, the UPDATE matches zero rows, the CTE
// returns empty, and the INSERT does nothing — we surface this as
// ErrGameNotFound so callers can distinguish it from a transient DB
// error.
func (r *PostgresRepo) AppendEvent(ctx context.Context, gameID, eventType string, payload json.RawMessage) (int, error) {
	row := r.pool.QueryRowContext(ctx, `
		WITH s AS (
			UPDATE games SET event_seq = event_seq + 1
			WHERE id = $1 RETURNING event_seq
		)
		INSERT INTO game_events (game_id, seq, type, payload)
		SELECT $1, s.event_seq, $2, $3 FROM s
		RETURNING seq
	`, gameID, eventType, []byte(payload))
	var seq int
	if err := row.Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrGameNotFound
		}
		return 0, fmt.Errorf("append event: %w", err)
	}
	return seq, nil
}

func (r *PostgresRepo) LoadEvent(ctx context.Context, gameID string, seq int) (EventRow, error) {
	row := r.pool.QueryRowContext(ctx, `
		SELECT seq, type, payload FROM game_events
		WHERE game_id = $1 AND seq = $2
	`, gameID, seq)
	var ev EventRow
	var payload []byte
	if err := row.Scan(&ev.Seq, &ev.Type, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EventRow{}, nil
		}
		return EventRow{}, fmt.Errorf("load event: %w", err)
	}
	ev.Payload = json.RawMessage(payload)
	return ev, nil
}

func (r *PostgresRepo) EventsSince(ctx context.Context, gameID string, sinceSeq, limit int) ([]EventRow, error) {
	rows, err := r.pool.QueryContext(ctx, `
		SELECT seq, type, payload FROM game_events
		WHERE game_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT $3
	`, gameID, sinceSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("events since: %w", err)
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var ev EventRow
		var payload []byte
		if err := rows.Scan(&ev.Seq, &ev.Type, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		ev.Payload = json.RawMessage(payload)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) CurrentEventSeq(ctx context.Context, gameID string) (int, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT event_seq FROM games WHERE id = $1`, gameID)
	var seq int
	if err := row.Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrGameNotFound
		}
		return 0, fmt.Errorf("current event seq: %w", err)
	}
	return seq, nil
}

func (r *PostgresRepo) EnqueueMatchmake(ctx context.Context, userID string, players int, mode string, rating int) error {
	_, err := r.pool.ExecContext(ctx, `
		INSERT INTO matchmake_queue (user_id, players, mode, rating)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE
		SET players = EXCLUDED.players,
		    mode = EXCLUDED.mode,
		    rating = EXCLUDED.rating,
		    enqueued_at = NOW()
	`, userID, players, mode, rating)
	return err
}

func (r *PostgresRepo) CancelMatchmake(ctx context.Context, userID string) error {
	_, err := r.pool.ExecContext(ctx, `DELETE FROM matchmake_queue WHERE user_id = $1`, userID)
	return err
}

// matchmakeBatchSize caps how many rows one tick locks at once. Big
// enough to give the pairing logic something to work with, small enough
// that the row-level locks don't sit on the table for noticeable time.
const matchmakeBatchSize = 100

func (r *PostgresRepo) MatchmakeTick(
	ctx context.Context,
	players int,
	mode string,
	pairFn func([]QueuedUser) [][]QueuedUser,
) ([]MatchedSeat, error) {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// FOR UPDATE SKIP LOCKED is the load-bearing piece here. Multiple
	// pods can tick concurrently and each will grab disjoint rows —
	// the ones another pod is already processing get skipped on this
	// pass and picked up next tick by whoever wins the race.
	//
	// We left-join profiles so the display name lands with the row
	// (single round-trip, no per-user lookup later). Users without a
	// profile row get an empty string; the caller falls back to a
	// sensible default.
	rows, err := tx.QueryContext(ctx, `
		SELECT q.user_id, q.rating, q.enqueued_at, COALESCE(p.display_name, '')
		FROM matchmake_queue q
		LEFT JOIN profiles p ON p.user_id = q.user_id
		WHERE q.players = $1 AND q.mode = $2
		ORDER BY q.enqueued_at
		FOR UPDATE OF q SKIP LOCKED
		LIMIT $3
	`, players, mode, matchmakeBatchSize)
	if err != nil {
		return nil, fmt.Errorf("select queue: %w", err)
	}
	var queued []QueuedUser
	for rows.Next() {
		var qu QueuedUser
		if err := rows.Scan(&qu.UserID, &qu.Rating, &qu.EnqueuedAt, &qu.DisplayName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan queue row: %w", err)
		}
		queued = append(queued, qu)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(queued) < players {
		// Not enough for even one pairing this tick. Commit (releases
		// our locks) and wait for the next interval.
		return nil, tx.Commit()
	}

	groups := pairFn(queued)
	if len(groups) == 0 {
		return nil, tx.Commit()
	}

	var seats []MatchedSeat
	for _, g := range groups {
		if len(g) != players {
			// pairFn contract violation — skip rather than crash.
			continue
		}
		gameID := newID()
		cfg := game.DefaultConfig(players)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO games (id, status, board_side, capture_pairs_win, align4_to_win, align5_to_win, initial_time_ms, increment_ms, visibility)
			VALUES ($1, 'playing', $2, $3, $4, $5, $6, $7, 'public')
		`, gameID, cfg.BoardSide, cfg.CapturePairsWin, cfg.Align4ToWin, cfg.Align5ToWin, cfg.InitialTimeMs, cfg.IncrementMs); err != nil {
			return nil, fmt.Errorf("insert matched game: %w", err)
		}

		userIDs := make([]string, 0, len(g))
		for i, u := range g {
			token := newToken()
			name := u.DisplayName
			if name == "" {
				name = "Joueur"
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO seats (game_id, seat_index, color, name, token_hash, user_id, occupied, is_bot)
				VALUES ($1, $2, $3, $4, $5, $6, TRUE, FALSE)
			`, gameID, i, int(game.Color(i+1)), name, hashToken(token), u.UserID); err != nil {
				return nil, fmt.Errorf("insert matched seat: %w", err)
			}
			seats = append(seats, MatchedSeat{
				UserID:    u.UserID,
				GameID:    gameID,
				SeatIndex: i,
				Token:     token,
				Name:      name,
			})
			userIDs = append(userIDs, u.UserID)
		}

		// Drop the matched users' queue rows so the next tick won't
		// see them. The same tx commit that materialises the game also
		// removes them — no in-between state where users have no game
		// and no queue presence.
		//
		// Cast $1 to uuid[] explicitly: pgx serialises a Go []string as
		// text[], and there is no implicit text[]→uuid[] coercion for
		// the ANY comparison. The cast happens once per query, server
		// side, before the WHERE evaluates.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM matchmake_queue WHERE user_id = ANY($1::uuid[])
		`, userIDs); err != nil {
			return nil, fmt.Errorf("delete matched queue: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return seats, nil
}
