-- +goose Up

-- rematch_offer stores the in-flight per-seat acceptance state for a
-- chess.com-style rematch proposal on a finished game. Persisting it
-- is necessary because Gemline runs multi-pod (2+ replicas behind a
-- LoadBalancer): an offer created on pod A would be invisible to
-- pod B otherwise, since the cache invalidation that follows every
-- state event forces pod B to re-read from the DB and the offer was
-- never written there.
--
-- Shape is opaque JSON to the DB; the Go side serialises a struct
-- with {acceptedSeats: int->bool, createdAt: time}. NULL means "no
-- offer pending" — the common state for any game that isn't right
-- after a finish.
ALTER TABLE games
    ADD COLUMN rematch_offer JSONB;

-- +goose Down

ALTER TABLE games
    DROP COLUMN rematch_offer;
