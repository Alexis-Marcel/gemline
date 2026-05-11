-- +goose Up
CREATE TABLE games (
    id                  TEXT        PRIMARY KEY,
    status              TEXT        NOT NULL,
    board_side          INTEGER     NOT NULL,
    capture_pairs_win   INTEGER     NOT NULL,
    align4_to_win       INTEGER     NOT NULL,
    align5_to_win       INTEGER     NOT NULL,
    winner_color        INTEGER     NOT NULL DEFAULT 0,
    win_kind            INTEGER     NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX games_status_idx ON games (status);

CREATE TABLE seats (
    game_id     TEXT      NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    seat_index  INTEGER   NOT NULL,
    color       INTEGER   NOT NULL,
    name        TEXT      NOT NULL DEFAULT '',
    token_hash  BYTEA,
    occupied    BOOLEAN   NOT NULL DEFAULT FALSE,
    is_bot      BOOLEAN   NOT NULL DEFAULT FALSE,
    PRIMARY KEY (game_id, seat_index)
);

CREATE TABLE moves (
    game_id     TEXT        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    ordinal     INTEGER     NOT NULL,
    color       INTEGER     NOT NULL,
    q           INTEGER     NOT NULL,
    r           INTEGER     NOT NULL,
    played_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (game_id, ordinal)
);

-- +goose Down
DROP TABLE moves;
DROP TABLE seats;
DROP TABLE games;
