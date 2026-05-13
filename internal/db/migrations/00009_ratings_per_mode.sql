-- +goose Up

-- Per-user rating becomes per-(user, mode) rating: 1v1 and multi each get
-- their own row, like Bullet/Blitz/Rapide on chess.com. Existing rows all
-- come from 2-player matchmade games, so we backfill them as mode='1v1'.
ALTER TABLE ratings ADD COLUMN mode TEXT NOT NULL DEFAULT '1v1';

-- The PK changes from (user_id) to (user_id, mode). Postgres requires
-- dropping the old PK before adding the new composite one.
ALTER TABLE ratings DROP CONSTRAINT ratings_pkey;
ALTER TABLE ratings ADD PRIMARY KEY (user_id, mode);

-- Strip the default once existing rows are backfilled — new inserts must
-- name their mode explicitly so a future "blitz" rating can't accidentally
-- land in the 1v1 column.
ALTER TABLE ratings ALTER COLUMN mode DROP DEFAULT;

-- The leaderboard query now filters by mode, so the index covers mode +
-- rating DESC + updated_at DESC. Dropping the old index frees space.
DROP INDEX IF EXISTS ratings_rating_desc_idx;
CREATE INDEX ratings_mode_rating_desc_idx ON ratings (mode, rating DESC, updated_at DESC);

-- +goose Down

-- Going back to one row per user means picking which mode to keep. We
-- prefer 1v1 since that's the rating that existed before the split —
-- multi rows are deleted to make the new (user_id) PK unique-safe.
DROP INDEX IF EXISTS ratings_mode_rating_desc_idx;
DELETE FROM ratings WHERE mode <> '1v1';
ALTER TABLE ratings DROP CONSTRAINT ratings_pkey;
ALTER TABLE ratings ADD PRIMARY KEY (user_id);
ALTER TABLE ratings DROP COLUMN mode;
CREATE INDEX ratings_rating_desc_idx ON ratings (rating DESC, updated_at DESC);
