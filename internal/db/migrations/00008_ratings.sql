-- +goose Up

-- Per-user Elo rating. Rows are upserted lazily — the first finished rated
-- game involving the user creates the row. Defaults match chess.com's
-- starting rating + a K-factor of 32 applied by the application layer.
CREATE TABLE ratings (
    user_id    UUID        PRIMARY KEY,
    rating     INTEGER     NOT NULL DEFAULT 1200,
    games      INTEGER     NOT NULL DEFAULT 0,
    wins       INTEGER     NOT NULL DEFAULT 0,
    losses     INTEGER     NOT NULL DEFAULT 0,
    draws      INTEGER     NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Leaderboard query: top ratings, updated_at as tiebreak so the most
-- recently active sits first when ratings are tied.
CREATE INDEX ratings_rating_desc_idx ON ratings (rating DESC, updated_at DESC);

-- rated_at is the timestamp the game's rating update was applied. NULL means
-- the game has not (yet) contributed to Elo — either because it's still in
-- progress, ineligible (bot/private/multiplayer), or simply not processed
-- yet. The application uses a conditional UPDATE ... WHERE rated_at IS NULL
-- as a single-writer guard, so the rating math runs exactly once per game.
ALTER TABLE games ADD COLUMN rated_at TIMESTAMPTZ;

-- +goose Down

ALTER TABLE games DROP COLUMN rated_at;
DROP TABLE ratings;
