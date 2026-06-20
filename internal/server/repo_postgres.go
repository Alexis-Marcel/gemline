package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alexis-marcel/gemline/internal/game"
)

// PostgresRepo persists games to Postgres; schema in internal/db/migrations.
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
		       visibility, rematch_game_id, rematch_offer, draw_offer_by
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
		rematchOffer  []byte
		drawOfferBy   int
	)
	if err := row.Scan(&status, &boardSide, &captureWin, &align4, &align5, &winnerColor, &winKind, &initialTimeMs, &incrementMs, &createdAt, &visibility, &rematchID, &rematchOffer, &drawOfferBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("select game: %w", err)
	}

	// Seats first: the roster is needed to construct GameState.
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

	// Replay through ApplyMove so captures, wins, and clock deductions are
	// reproduced by the same engine; played_at is the move's "now" so each
	// TimeRemainingMs converges to its live value. Winner is NOT pre-seeded:
	// ApplyMove rejects moves once IsOver(), so a finished game would fail to
	// replay. The persisted outcome is overlaid after the loop instead.
	moveRows, err := r.pool.QueryContext(ctx, `
		SELECT color, q, r, played_at FROM moves WHERE game_id = $1 ORDER BY ordinal
	`, id)
	if err != nil {
		return nil, fmt.Errorf("select moves: %w", err)
	}
	defer moveRows.Close()

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
			state.TurnStartedAt = playedAt
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

	// In-progress game with no moves yet: restart the clock from now, else a
	// long-idle game would flag the active player immediately on load.
	if status == StatusPlaying && lastPlayed.IsZero() && state.ClockEnabled() {
		state.TurnStartedAt = time.Now()
	}

	// Overlay the DB outcome: replay covers move-driven wins, but resign /
	// timeout / draw need this. Idempotent for move-driven endings.
	state.Winner = game.Color(winnerColor)
	state.WinKind = game.WinKind(winKind)

	// Decode rematch_offer if present. A corrupt blob is non-fatal: the offer
	// is recoverable (re-click), and shouldn't block loading the game.
	var offer *RematchOffer
	if len(rematchOffer) > 0 {
		var parsed RematchOffer
		if err := json.Unmarshal(rematchOffer, &parsed); err == nil {
			if parsed.AcceptedSeats == nil {
				parsed.AcceptedSeats = make(map[int]bool)
			}
			offer = &parsed
		}
	}
	return &GameRecord{
		ID:            id,
		State:         state,
		Seats:         seats,
		Status:        status,
		Visibility:    Visibility(visibility),
		RematchGameID: rematchID.String,
		RematchOffer:  offer,
		CreatedAt:     createdAt,
		DrawOfferBy:   drawOfferBy,
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

// nilIfEmpty maps an empty slice to nil so it stores as SQL NULL, not a
// zero-length bytea (which compares differently).
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

// EnsureProfile is the create-only sibling of UpsertProfile: ON CONFLICT DO
// NOTHING, so it never overwrites a chosen display_name.
func (r *PostgresRepo) EnsureProfile(ctx context.Context, userID, fallbackName string) error {
	_, err := r.pool.ExecContext(ctx, `
		INSERT INTO profiles (user_id, display_name) VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING
	`, userID, fallbackName)
	return err
}

func (r *PostgresRepo) GamesForUser(ctx context.Context, userID string, limit int) ([]UserGame, error) {
	// History shows finished games only; in-progress or abandoned ones don't
	// belong on a "what you played" list.
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
			ug        UserGame
			color     int
			winner    int
			createdAt time.Time
			updatedAt time.Time
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
	// Per-mode ratings via correlated subqueries with the default baked in, to
	// keep this a single round-trip.
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
	// Fetch the most-recent limit rows, then reverse to chronological order.
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
			m      Message
			color  int
			sentAt time.Time
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
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (r *PostgresRepo) LobbyGames(ctx context.Context, limit int) ([]LobbyEntry, error) {
	// Public waiting games with denormalised seat counts so the lobby shows
	// "1/2" without shipping the seat list. Index: games_public_waiting_idx.
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
	// COALESCE preserves an already-set link, so the racing second writer is a
	// no-op and RETURNING hands back the winning ID without an extra SELECT.
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
		// COALESCE should make this non-null on any successful UPDATE; tolerate.
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
	// applies the math; a later caller gets no row and bails without
	// double-crediting Elo.
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
		// Record the delta in rating_history (PK game_id,user_id blocks double
		// inserts) in the same tx as the ratings update so they can't desync.
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

// RatingsForGame builds the per-seat rating snapshot keyed by game ID.
// Rated=false (empty Seats) for ineligible games (non-public, or any bot/anon
// seat); Applied is true once rated_at is set, which adds the history fields.
func (r *PostgresRepo) RatingsForGame(ctx context.Context, gameID string) (GameRatings, error) {
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

	// LEFT JOIN history — rows are absent for in-progress games.
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
			seatIdx    int
			userID     sql.NullString
			currentRtg int
			oldRtg     sql.NullInt32
			newRtg     sql.NullInt32
			delta      sql.NullInt32
			result     sql.NullString
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
	// INNER JOIN profiles drops users with no display name; filtered by mode.
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

// PublicProfile aggregates a user's public stats from the canonical tables.
// Counts are scoped to finished games so a half-played room doesn't pad them.
func (r *PostgresRepo) PublicProfile(ctx context.Context, userID string) (PublicProfileSummary, error) {
	// No profile row means no display name to render — treat as not found.
	row := r.pool.QueryRowContext(ctx, `SELECT display_name FROM profiles WHERE user_id = $1`, userID)
	var displayName string
	if err := row.Scan(&displayName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublicProfileSummary{}, ErrProfileNotFound
		}
		return PublicProfileSummary{}, fmt.Errorf("profile lookup: %w", err)
	}

	// Win/lost/draw filters mirror StatsForUser; kept in sync by hand.
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

// SearchProfiles returns profiles whose display_name starts with prefix
// (case-insensitive), with 1v1 Elo joined in. Empty prefix returns nothing and
// limit is clamped, so the endpoint can't scrape the whole table.
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

// DeleteStaleWaitingGames removes waiting games idle longer than olderThan.
// Freshness is updated_at, not created_at, so a host still adjusting their
// lobby today isn't reaped even if the game was created last week.
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

// AppendEvent inserts a game_events row with a monotonic per-game seq. The CTE
// bumps games.event_seq and feeds it to the INSERT in one statement; the
// row-level lock on games.id serializes concurrent writers. A missing gameID
// matches zero rows and surfaces as ErrGameNotFound.
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

// matchmakeBatchSize caps rows locked per tick: enough to pair against, few
// enough that the row locks don't sit on the table noticeably.
const matchmakeBatchSize = 100

// SaveRematchOffer writes (encoded body) or clears (nil) the rematch_offer
// JSONB. A zero-row update surfaces as ErrGameNotFound rather than silently
// swallowing a deleted game.
func (r *PostgresRepo) SaveRematchOffer(ctx context.Context, gameID string, offer []byte) error {
	var blob interface{}
	if offer != nil {
		blob = offer
	}
	res, err := r.pool.ExecContext(ctx, `
		UPDATE games SET rematch_offer = $2 WHERE id = $1
	`, gameID, blob)
	if err != nil {
		return fmt.Errorf("save rematch offer: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrGameNotFound
	}
	return nil
}

// MergeRematchAcceptance atomically merges seatIdx's acceptance into the
// persisted rematch_offer. The read and write run inside a transaction with
// SELECT ... FOR UPDATE on the games row, so two pods racing on the same
// game serialise at the DB layer: the second one sees the first's offer
// (including the first's seat) and adds itself, instead of overwriting it
// with a fresh single-seat offer — which was the cause of the
// "accept does nothing" rematch bug on multi-pod deploys.
func (r *PostgresRepo) MergeRematchAcceptance(ctx context.Context, gameID string, seatIdx int, botSeats []int) (*RematchOffer, error) {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current []byte
	err = tx.QueryRowContext(ctx,
		`SELECT rematch_offer FROM games WHERE id = $1 FOR UPDATE`, gameID,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGameNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select rematch offer: %w", err)
	}

	var offer RematchOffer
	if len(current) > 0 {
		if err := json.Unmarshal(current, &offer); err != nil {
			return nil, fmt.Errorf("unmarshal rematch offer: %w", err)
		}
		if offer.AcceptedSeats == nil {
			offer.AcceptedSeats = make(map[int]bool)
		}
	} else {
		offer = RematchOffer{
			AcceptedSeats: make(map[int]bool, len(botSeats)+1),
			CreatedAt:     time.Now(),
		}
		for _, b := range botSeats {
			offer.AcceptedSeats[b] = true
		}
	}
	offer.AcceptedSeats[seatIdx] = true

	blob, err := json.Marshal(&offer)
	if err != nil {
		return nil, fmt.Errorf("marshal rematch offer: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE games SET rematch_offer = $2 WHERE id = $1`, gameID, blob,
	); err != nil {
		return nil, fmt.Errorf("update rematch offer: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit rematch offer: %w", err)
	}
	return &offer, nil
}

// SaveDrawOffer writes the offering seat index (or -1 to clear). Without it, an
// opponent's accept on a non-originating pod would reload a DB row unaware of
// the offer.
func (r *PostgresRepo) SaveDrawOffer(ctx context.Context, gameID string, offerBy int) error {
	res, err := r.pool.ExecContext(ctx, `
		UPDATE games SET draw_offer_by = $2 WHERE id = $1
	`, gameID, offerBy)
	if err != nil {
		return fmt.Errorf("save draw offer: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrGameNotFound
	}
	return nil
}

// MatchmakeQueueSnapshot returns the queue rows without locking, for the
// matcher's post-tick queue_update events.
func (r *PostgresRepo) MatchmakeQueueSnapshot(ctx context.Context, players int, mode string) ([]QueuedUser, error) {
	rows, err := r.pool.QueryContext(ctx, `
		SELECT q.user_id, q.rating, q.enqueued_at, COALESCE(p.display_name, '')
		FROM matchmake_queue q
		LEFT JOIN profiles p ON p.user_id = q.user_id
		WHERE q.players = $1 AND q.mode = $2
		ORDER BY q.enqueued_at
	`, players, mode)
	if err != nil {
		return nil, fmt.Errorf("queue snapshot: %w", err)
	}
	defer rows.Close()
	var out []QueuedUser
	for rows.Next() {
		var qu QueuedUser
		if err := rows.Scan(&qu.UserID, &qu.Rating, &qu.EnqueuedAt, &qu.DisplayName); err != nil {
			return nil, fmt.Errorf("scan queue snapshot row: %w", err)
		}
		out = append(out, qu)
	}
	return out, rows.Err()
}

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

	// FOR UPDATE SKIP LOCKED lets pods tick concurrently on disjoint rows:
	// rows another pod is processing are skipped this pass and picked up next.
	// LEFT JOIN profiles brings the display name in the same round-trip.
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

	// Don't pre-filter on len < players: that used to short-circuit before
	// pairFn ran, breaking partial multi rooms (3 queued for players=6).
	groups := pairFn(queued)
	if len(groups) == 0 {
		return nil, tx.Commit()
	}

	var seats []MatchedSeat
	for _, g := range groups {
		// Defensive: drop a malformed group rather than crash (pairFn shouldn't
		// emit one).
		if len(g) < 2 || len(g) > players {
			continue
		}
		gameID := newID()
		actualPlayers := len(g)
		cfg := game.DefaultConfig(actualPlayers)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO games (id, status, board_side, capture_pairs_win, align4_to_win, align5_to_win, initial_time_ms, increment_ms, visibility)
			VALUES ($1, 'playing', $2, $3, $4, $5, $6, $7, 'public')
		`, gameID, cfg.BoardSide, cfg.CapturePairsWin, cfg.Align4ToWin, cfg.Align5ToWin, cfg.InitialTimeMs, cfg.IncrementMs); err != nil {
			return nil, fmt.Errorf("insert matched game: %w", err)
		}

		userIDs := make([]string, 0, len(g))
		for i, u := range g {
			name := u.DisplayName
			if name == "" {
				name = "Joueur"
			}
			// Reserved by identity, no token — the player resolves it by JWT on
			// arrival (ResolveSeat), same as a rematch seat.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO seats (game_id, seat_index, color, name, token_hash, user_id, occupied, is_bot)
				VALUES ($1, $2, $3, $4, NULL, $5, TRUE, FALSE)
			`, gameID, i, int(game.Color(i+1)), name, u.UserID); err != nil {
				return nil, fmt.Errorf("insert matched seat: %w", err)
			}
			seats = append(seats, MatchedSeat{UserID: u.UserID, GameID: gameID})
			userIDs = append(userIDs, u.UserID)
		}

		// Drop queue rows in the same tx that creates the game, so there's no
		// state where users have neither game nor queue presence. The ::uuid[]
		// cast is required: pgx sends []string as text[], which has no implicit
		// coercion to uuid[] for the ANY comparison.
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

func (r *PostgresRepo) CurrentMatchmadeGame(ctx context.Context, userID string) (string, error) {
	var id string
	err := r.pool.QueryRowContext(ctx, `
		SELECT g.id
		FROM games g
		JOIN seats s ON s.game_id = g.id
		WHERE s.user_id = $1 AND s.occupied
		  AND g.visibility = 'public' AND g.status <> 'finished'
		ORDER BY g.created_at DESC
		LIMIT 1
	`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("current matchmade game: %w", err)
	}
	return id, nil
}
