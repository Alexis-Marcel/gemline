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
	_, err = tx.ExecContext(ctx, `
		INSERT INTO games (id, status, board_side, capture_pairs_win, align4_to_win, align5_to_win)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, rec.ID, rec.Status, cfg.BoardSide, cfg.CapturePairsWin, cfg.Align4ToWin, cfg.Align5ToWin)
	if err != nil {
		return fmt.Errorf("insert game: %w", err)
	}

	for _, s := range rec.Seats {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO seats (game_id, seat_index, color, name, token_hash, occupied, is_bot)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, rec.ID, s.Index, int(s.Color), s.Name, nilIfEmpty(s.TokenHash), s.Occupied, s.IsBot)
		if err != nil {
			return fmt.Errorf("insert seat %d: %w", s.Index, err)
		}
	}
	return tx.Commit()
}

func (r *PostgresRepo) LoadGame(ctx context.Context, id string) (*GameRecord, error) {
	row := r.pool.QueryRowContext(ctx, `
		SELECT status, board_side, capture_pairs_win, align4_to_win, align5_to_win,
		       winner_color, win_kind, created_at
		FROM games WHERE id = $1
	`, id)
	var (
		status       Status
		boardSide    int
		captureWin   int
		align4       int
		align5       int
		winnerColor  int
		winKind      int
		createdAt    time.Time
	)
	if err := row.Scan(&status, &boardSide, &captureWin, &align4, &align5, &winnerColor, &winKind, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("select game: %w", err)
	}

	// Seats — needed to know the player roster before we can construct GameState.
	seatRows, err := r.pool.QueryContext(ctx, `
		SELECT seat_index, color, name, token_hash, occupied, is_bot
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
			occupied  bool
			isBot     bool
		)
		if err := seatRows.Scan(&idx, &colorInt, &name, &tokenHash, &occupied, &isBot); err != nil {
			return nil, fmt.Errorf("scan seat: %w", err)
		}
		seats = append(seats, Seat{
			Index:     idx,
			Color:     game.Color(colorInt),
			Name:      name,
			TokenHash: tokenHash,
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
	}
	state := game.NewGame(colors, cfg)

	// Replay moves through ApplyMove so captures and wins are reproduced from
	// the rule engine (single source of truth for derived state).
	moveRows, err := r.pool.QueryContext(ctx, `
		SELECT color, q, r FROM moves WHERE game_id = $1 ORDER BY ordinal
	`, id)
	if err != nil {
		return nil, fmt.Errorf("select moves: %w", err)
	}
	defer moveRows.Close()

	for moveRows.Next() {
		var (
			colorInt int
			q, rr    int
		)
		if err := moveRows.Scan(&colorInt, &q, &rr); err != nil {
			return nil, fmt.Errorf("scan move: %w", err)
		}
		if _, err := state.ApplyMove(game.Move{
			Player: game.Color(colorInt),
			Pos:    game.Position{Q: q, R: rr},
		}); err != nil {
			return nil, fmt.Errorf("replay move: %w", err)
		}
	}
	if err := moveRows.Err(); err != nil {
		return nil, err
	}

	return &GameRecord{
		ID:        id,
		State:     state,
		Seats:     seats,
		Status:    status,
		CreatedAt: createdAt,
	}, nil
}

func (r *PostgresRepo) UpdateSeat(ctx context.Context, gameID string, seat *Seat, status Status) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		UPDATE seats SET name = $1, token_hash = $2, occupied = $3, is_bot = $4
		WHERE game_id = $5 AND seat_index = $6
	`, seat.Name, seat.TokenHash, seat.Occupied, seat.IsBot, gameID, seat.Index)
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
