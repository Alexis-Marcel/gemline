-- +goose Up

-- Singleton leadership leases (leader election), one row per elected role —
-- today just 'matchmaker'. Same mechanics as game_leases (DB-clock expiry,
-- heartbeat renewal, epoch bumped on every change of hands) but keyed by a
-- role name instead of a game, hence no FK.

CREATE TABLE leader_leases (
    name       TEXT        PRIMARY KEY,
    owner_id   TEXT        NOT NULL,
    epoch      BIGINT      NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE leader_leases;
