-- +goose Up

-- Bring the win-threshold columns of still-active games in line with the
-- new DefaultConfig (matches the printed rulebook). Finished games are
-- left untouched: their outcomes were sealed under the old rules and
-- rewriting their config would invalidate the persisted winner/win_kind
-- and the move-replay reconstruction.
--
-- All thresholds in the new table are higher than the old ones, so a
-- playing game's history never produced a win under the new rules either
-- — replay through ApplyMove stays consistent.
UPDATE games AS gm
SET capture_pairs_win = CASE sc.n WHEN 2 THEN 12 WHEN 3 THEN 10 WHEN 4 THEN 9 WHEN 5 THEN 7 WHEN 6 THEN 6 ELSE gm.capture_pairs_win END,
    align4_to_win     = CASE sc.n WHEN 2 THEN 8  WHEN 3 THEN 6  WHEN 4 THEN 5 WHEN 5 THEN 4 WHEN 6 THEN 4 ELSE gm.align4_to_win END,
    align5_to_win     = CASE sc.n WHEN 2 THEN 3  WHEN 3 THEN 3  WHEN 4 THEN 2 WHEN 5 THEN 2 WHEN 6 THEN 2 ELSE gm.align5_to_win END
FROM (SELECT game_id, COUNT(*) AS n FROM seats GROUP BY game_id) AS sc
WHERE gm.id = sc.game_id
  AND gm.status IN ('waiting', 'playing');

-- +goose Down

-- The pre-migration values are not recoverable from the row alone (we'd
-- need to know which games predated this migration). Down is intentionally
-- a no-op — rolling back the schema does not rewrite gameplay state. Data
-- written by the new code remains forward-compatible with the old engine,
-- it just makes those games harder to win.
SELECT 1;
