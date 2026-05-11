-- +goose Up

-- Profile data we control, keyed by the Supabase auth.users(id) UUID. We
-- intentionally do NOT add a foreign key to auth.users — that table lives
-- in a Supabase-managed schema and we want our migrations to work against
-- any plain Postgres for tests.
CREATE TABLE profiles (
    user_id      UUID        PRIMARY KEY,
    display_name TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Link a seat to an authenticated user. NULL means the seat is held by a
-- guest (anonymous join) and contributes to no one's history.
ALTER TABLE seats
    ADD COLUMN user_id UUID;

CREATE INDEX seats_user_id_idx ON seats (user_id);

-- +goose Down
DROP INDEX seats_user_id_idx;
ALTER TABLE seats DROP COLUMN user_id;
DROP TABLE profiles;
