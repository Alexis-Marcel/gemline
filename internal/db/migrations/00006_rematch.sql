-- +goose Up

-- rematch_game_id links a finished game to the fresh game spawned by
-- POST /api/games/{id}/rematch. The FK is ON DELETE SET NULL so removing
-- the rematch row doesn't cascade-delete the original game record.
ALTER TABLE games
    ADD COLUMN rematch_game_id TEXT REFERENCES games (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE games
    DROP COLUMN rematch_game_id;
