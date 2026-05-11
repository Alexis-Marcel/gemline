-- +goose Up
CREATE TABLE messages (
    id           BIGSERIAL   PRIMARY KEY,
    game_id      TEXT        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    seat_index   INTEGER     NOT NULL,
    -- Denormalised author info: copied at write-time so a renamed or recycled
    -- seat can't rewrite history.
    author_color INTEGER     NOT NULL,
    author_name  TEXT        NOT NULL,
    body         TEXT        NOT NULL,
    sent_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX messages_game_sent_idx ON messages (game_id, sent_at);

-- +goose Down
DROP INDEX messages_game_sent_idx;
DROP TABLE messages;
