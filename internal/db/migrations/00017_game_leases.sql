-- +goose Up

-- Per-game ownership lease: at most one pod owns a live game at a time.
-- Ownership is time-bound — the owner keeps expires_at fresh via heartbeat,
-- and a lease past its expiry is up for grabs, so a crashed owner is
-- superseded after the TTL with no cleanup. Expiry is always compared
-- against the database clock (NOW()), the one time reference every pod
-- shares, so pod clock skew is irrelevant.
--
-- epoch increments each time ownership changes hands (never on renewal).
-- It is the fencing token: writes will carry it (step 1c) so a former
-- owner that froze past its own expiry gets rejected instead of racing
-- the new owner.

CREATE TABLE game_leases (
    game_id    TEXT        PRIMARY KEY REFERENCES games (id) ON DELETE CASCADE,
    owner_id   TEXT        NOT NULL,
    epoch      BIGINT      NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE game_leases;
