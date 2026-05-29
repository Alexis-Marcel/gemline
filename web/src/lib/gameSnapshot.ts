import type { Game } from "../api/types";

/**
 * Picks the freshest game view between the WS `live` snapshot and the
 * caller's `optimistic` one (their own HTTP mutation before the WS event
 * lands). Optimistic wins ONLY with strictly more moves; on ties we defer
 * to live because many server transitions (waiting→playing, draw/rematch
 * state, presence) don't bump moveCount — a `>=` here masked those until a
 * refresh (bug fixed in 64aba2a). So optimistic only ever surfaces the
 * caller's just-played move for the few ms before the WS state event.
 */
export function mergeGameSnapshot(
  live: Game | null,
  optimistic: Game | null,
): Game | null {
  if (!optimistic) return live;
  if (!live) return optimistic;
  return optimistic.moveCount > live.moveCount ? optimistic : live;
}
