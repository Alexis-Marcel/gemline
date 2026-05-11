-- +goose Up

ALTER TABLE games
    ADD COLUMN initial_time_ms BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN increment_ms    BIGINT NOT NULL DEFAULT 0;

-- Existing rows keep the no-clock default (0/0). New games written by the
-- application will pass non-zero values via DefaultConfig.

-- +goose Down

ALTER TABLE games
    DROP COLUMN increment_ms,
    DROP COLUMN initial_time_ms;
