-- +goose Up

-- Per-game record of every Elo update applied to a player. The ratings
-- table only holds the current rating; this one captures the delta so
-- the end-of-game UI can show "+12" / "-8" next to each player and the
-- API can answer "what did this match cost you" hours after the fact.
--
-- Populated from the same transaction as ApplyRatedGame's UPDATE on
-- ratings, so a row exists iff the rating was applied (no half-state
-- where ratings drifted but the history is empty).

CREATE TABLE rating_history (
    game_id     TEXT        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    user_id     UUID        NOT NULL,
    old_rating  INTEGER     NOT NULL,
    new_rating  INTEGER     NOT NULL,
    delta       INTEGER     NOT NULL,
    result      CHAR(1)     NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (game_id, user_id)
);

CREATE INDEX rating_history_user_idx ON rating_history (user_id, applied_at DESC);

-- +goose Down
DROP TABLE rating_history;
