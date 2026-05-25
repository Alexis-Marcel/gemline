import type { Game } from "../api/types";

/**
 * mergeGameSnapshot picks the freshest view of a game between the
 * server-pushed `live` snapshot (from the per-game WebSocket) and the
 * caller's `optimistic` snapshot (returned by their own HTTP mutation
 * before the WS state event has caught up).
 *
 * The rule: optimistic wins ONLY if it has strictly more moves than
 * live. On ties we MUST defer to live, because many server-side
 * transitions don't bump moveCount — waiting → playing on AllSeated,
 * draw offer set / cleared, seat invitations, rematch state, presence
 * tweaks. A `>=` here used to mask those updates until a refresh, see
 * the bug fixed in commit 64aba2a.
 *
 * In practice this means optimistic state is only ever surfaced for the
 * very next move the caller just played, before the WS state event
 * arrives (typically a few ms). For every non-move action, optimistic
 * is set but immediately superseded by live — that's intentional: the
 * setter still gives an API mismatch a chance to surface as an updated
 * DTO (e.g. action rejected, error path), it just doesn't override
 * server reality.
 */
export function mergeGameSnapshot(
  live: Game | null,
  optimistic: Game | null,
): Game | null {
  if (!optimistic) return live;
  if (!live) return optimistic;
  return optimistic.moveCount > live.moveCount ? optimistic : live;
}
