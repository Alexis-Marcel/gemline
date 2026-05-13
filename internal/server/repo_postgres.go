package server

import (
	"context"
	"database/sql"
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
	state.Winner = game.Color(winnerColor)
	state.WinKind = game.WinKind(winKind)

	// Replay moves through ApplyMove so captures, wins, AND chess-clock
	// deductions are reproduced from the same rule engine that produced
	// them at play time. played_at is passed as the move's "now" so each
	// player's TimeRemainingMs converges to the same value it had live.
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

func (r *PostgresRepo) GamesForUser(ctx context.Context, userID string, limit int) ([]UserGame, error) {
	rows, err := r.pool.QueryContext(ctx, `
		SELECT g.id, g.status, s.seat_index, s.color, g.winner_color,
		       (SELECT COUNT(*) FROM moves m WHERE m.game_id = g.id),
		       g.created_at, g.updated_at
		FROM seats s
		JOIN games g ON g.id = s.game_id
		WHERE s.user_id = $1
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
	row := r.pool.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) AS total,
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color = s.color) AS won,
		  COUNT(*) FILTER (WHERE g.status = 'finished' AND g.winner_color <> s.color AND g.winner_color <> 0) AS lost,
		  COUNT(*) FILTER (WHERE g.status <> 'finished') AS ongoing
		FROM seats s
		JOIN games g ON g.id = s.game_id
		WHERE s.user_id = $1
	`, userID)
	var st UserStats
	if err := row.Scan(&st.Total, &st.Won, &st.Lost, &st.Ongoing); err != nil {
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
