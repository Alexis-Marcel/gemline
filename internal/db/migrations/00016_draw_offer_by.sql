-- +goose Up

-- draw_offer_by stores the seat index of the player currently offering a
-- draw on an in-progress 2-player game, or -1 when no offer is pending.
-- Persisting it is necessary because Gemline runs multi-pod (2+ replicas
-- behind a LoadBalancer): an offer recorded on pod A would be invisible
-- to pod B otherwise, since the cache invalidation that follows every
-- state event forces pod B to re-read from the DB and the offer was
-- never written there. The opponent's POST /draw/accept then hits a
-- "no offer pending" branch and 409s if the load balancer routes it to
-- a non-originating pod.
--
-- SMALLINT covers the -1..N-1 range we need (N ≤ 6 by engine config).
-- Default -1 matches the in-memory sentinel; on restart any in-flight
-- offer is allowed to survive — losing it would just mean the proposer
-- has to re-click, which is no worse than the pre-migration behaviour.
ALTER TABLE games
    ADD COLUMN draw_offer_by SMALLINT NOT NULL DEFAULT -1;

-- +goose Down

ALTER TABLE games
    DROP COLUMN draw_offer_by;
