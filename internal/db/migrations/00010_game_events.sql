-- +goose Up

-- Per-game append-only event log. Every broadcast (state, move, chat,
-- presence) gets a row here with a monotonic per-game `seq`, before
-- being emitted via NOTIFY 'gemline_events'. This is the source of
-- truth for "what the client should see next" — the NOTIFY payload is
-- just a wake-up signal carrying {gameId, seq}, and listeners read the
-- payload back from this table. Clients that miss live events resync
-- via GET /api/games/:id/events?since=N.
--
-- `event_seq` lives on the games row and is incremented in the same
-- statement that inserts the event:
--   WITH s AS (
--     UPDATE games SET event_seq = event_seq + 1
--     WHERE id = $1 RETURNING event_seq
--   )
--   INSERT INTO game_events (game_id, seq, type, payload)
--   SELECT $1, s.event_seq, $2, $3 FROM s
--   RETURNING seq;
-- The row-level lock on games.id serializes concurrent inserts for the
-- same game without explicit locking on our side.

ALTER TABLE games ADD COLUMN event_seq INTEGER NOT NULL DEFAULT 0;

CREATE TABLE game_events (
    game_id     TEXT        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    seq         INTEGER     NOT NULL,
    type        TEXT        NOT NULL,
    payload     JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (game_id, seq)
);

-- +goose Down
DROP TABLE game_events;
ALTER TABLE games DROP COLUMN event_seq;
