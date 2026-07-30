-- +goose Up

-- Where to reach the owner: the internal listener sibling pods dial to
-- forward game commands (step 1b). Advertised by the pod itself at acquire
-- time; '' means the owner predates forwarding and callers fall back to
-- handling locally.
ALTER TABLE game_leases ADD COLUMN owner_addr TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE game_leases DROP COLUMN owner_addr;
