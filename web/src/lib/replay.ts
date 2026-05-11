import type { Color, ReplayStep } from "../api/types";
import { cellIndex, inBoard } from "./hex";

/**
 * cellsAtStep returns what the board's cell array looks like after `step`
 * steps of the replay have been applied (`step = 0` means the empty board,
 * `step = steps.length` means the final position).
 *
 * Captures are honoured: removed stones become Empty again in the array.
 */
export function cellsAtStep(
  side: number,
  steps: ReplayStep[],
  step: number,
): Color[] {
  const span = 2 * side - 1;
  const cells = new Array<Color>(span * span);
  for (let r = -(side - 1); r <= side - 1; r++) {
    for (let q = -(side - 1); q <= side - 1; q++) {
      cells[cellIndex(q, r, side)] = inBoard(q, r, side) ? 0 : -1;
    }
  }
  const upto = Math.max(0, Math.min(step, steps.length));
  for (let i = 0; i < upto; i++) {
    const s = steps[i];
    cells[cellIndex(s.q, s.r, side)] = s.player;
    for (const cap of s.captures) {
      for (const [q, r] of cap.pair) {
        cells[cellIndex(q, r, side)] = 0;
      }
    }
  }
  return cells;
}

/** Returns the just-placed coordinate for step N (1-indexed), or null. */
export function lastMoveAt(steps: ReplayStep[], step: number): { q: number; r: number } | null {
  if (step <= 0 || step > steps.length) return null;
  const s = steps[step - 1];
  return { q: s.q, r: s.r };
}
