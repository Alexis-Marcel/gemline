-- +goose Up

-- Players waiting to be matched. The batch matcher (running on every
-- pod, ticking every ~1.5s) pulls rows via SELECT ... FOR UPDATE SKIP
-- LOCKED, pairs them by rating proximity within an age-widened band,
-- creates games + seats, then DELETEs the matched rows and NOTIFYs the
-- affected users via the 'gemline_lobby' channel.
--
-- SKIP LOCKED is what lets every pod run the matcher with zero
-- coordination: they each grab disjoint batches. No leader election,
-- no single point of failure — if a pod crashes mid-tick, the row
-- lock is released on rollback and the next pod's tick picks the row up.
--
-- PK on user_id means a user can hold at most one active ticket; if
-- they click "find match" again while already queued, we upsert their
-- row via ON CONFLICT DO UPDATE rather than erroring or stacking
-- duplicate tickets.

CREATE TABLE matchmake_queue (
    user_id     TEXT        NOT NULL,
    players     INTEGER     NOT NULL,
    mode        TEXT        NOT NULL,
    rating      INTEGER     NOT NULL,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id)
);

-- The matcher selects by (players, mode) and orders by enqueued_at so
-- the oldest waiters get paired first and tolerance bands widen with
-- their age in queue.
CREATE INDEX matchmake_queue_pairing_idx
    ON matchmake_queue (players, mode, enqueued_at);

-- +goose Down
DROP TABLE matchmake_queue;
