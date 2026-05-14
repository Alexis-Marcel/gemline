-- +goose Up

-- Backfill: every user who has earned a rating but never had a
-- profiles row gets one now, so the leaderboard (which INNER JOINs
-- on profiles) stops dropping them silently. Historically, profiles
-- were only created via PUT /api/profile — users who matchmade and
-- played rated games without ever visiting the profile page have
-- ratings rows but no profile row, and were invisible on the board
-- as a result.
--
-- We seed display_name from any seats row the user occupied (the
-- seat name was already filled from displayNameFor at game-create
-- time: profile → email local-part → 'Joueur'). Multiple matches
-- pick an arbitrary one — users can change it via PUT /api/profile.
-- ON CONFLICT DO NOTHING is belt-and-suspenders against re-running.

INSERT INTO profiles (user_id, display_name)
SELECT r.user_id,
       COALESCE(
           NULLIF((
               SELECT s.name
               FROM seats s
               WHERE s.user_id = r.user_id AND s.name <> ''
               ORDER BY s.game_id
               LIMIT 1
           ), ''),
           'Joueur'
       )
FROM (SELECT DISTINCT user_id FROM ratings) r
LEFT JOIN profiles p ON p.user_id = r.user_id
WHERE p.user_id IS NULL
ON CONFLICT (user_id) DO NOTHING;

-- +goose Down

-- No automatic rollback: we don't track which rows the backfill
-- created vs which the user later edited. A blind DELETE would
-- erase legitimate user-supplied names. Manual cleanup if ever
-- needed.
