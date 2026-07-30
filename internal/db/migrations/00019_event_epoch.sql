-- +goose Up

-- Fencing: each journal append records the lease epoch it was written under.
-- AppendEvent's SQL refuses an append whose epoch no longer matches the
-- current lease, so a former owner that froze past its expiry (GC pause,
-- partition) is rejected at the write instead of racing the new owner.
-- 0 = unfenced: in-memory mode, and the deliberate handle-locally fallbacks,
-- which stay safe through plain row-lock serialization as before affinity.
ALTER TABLE game_events ADD COLUMN epoch BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE game_events DROP COLUMN epoch;
