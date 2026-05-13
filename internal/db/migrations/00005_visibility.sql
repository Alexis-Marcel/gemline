-- +goose Up

-- visibility controls discovery in the public lobby. 'private' (default) keeps
-- the legacy behavior: only people with the URL can join. 'public' makes the
-- game show up in GET /api/games/lobby while it's still in 'waiting' state.
ALTER TABLE games
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private';

-- Lobby query filters by status + visibility, ordered by created_at — the
-- partial index covers exactly that path.
CREATE INDEX games_public_waiting_idx
    ON games (created_at DESC)
    WHERE status = 'waiting' AND visibility = 'public';

-- +goose Down

DROP INDEX games_public_waiting_idx;

ALTER TABLE games
    DROP COLUMN visibility;
