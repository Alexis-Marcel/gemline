-- +goose Up

-- Bug fix for PR #11: matchmake_queue.user_id shipped as TEXT, but
-- profiles.user_id is UUID. The matcher's SELECT does a
--   LEFT JOIN profiles p ON p.user_id = q.user_id
-- which Postgres rejects with "operator does not exist: uuid = text"
-- (SQLSTATE 42883) because there is no implicit uuid=text cast. Every
-- matcher tick has been failing since #11 deployed; matchmaking is
-- effectively dead in prod.
--
-- Aligning the column to UUID closes the bug and keeps the index on
-- profiles.user_id usable in the join (no per-row cast). The USING
-- clause tells Postgres how to convert existing rows; the table is
-- expected to be empty (the bug above prevented any successful match,
-- so anyone enqueued earlier should have rolled back / left the queue),
-- but the cast handles non-empty cases gracefully too.
--
-- ALTER TYPE takes an ACCESS EXCLUSIVE lock and rewrites the table,
-- which is microseconds at our scale.

ALTER TABLE matchmake_queue
    ALTER COLUMN user_id TYPE UUID USING user_id::UUID;

-- +goose Down

-- Rolling back to TEXT would re-introduce the bug. Provided for
-- completeness only; do not use in prod.
ALTER TABLE matchmake_queue
    ALTER COLUMN user_id TYPE TEXT USING user_id::TEXT;
