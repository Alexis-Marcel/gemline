import { useEffect, useState } from "react";

interface PlayerClockProps {
  /** Server-reported time remaining in ms (frozen at the last snapshot). */
  remainingMs: number;
  /** ISO timestamp when the active player's turn began, or undefined if not their turn / no clock. */
  turnStartedAt: string | undefined;
  /** Whether this player's clock should be ticking now. */
  isActive: boolean;
  /** Whether the game is finished (clock should freeze). */
  frozen: boolean;
}

/**
 * PlayerClock renders an mm:ss countdown. When `isActive` is true and the
 * game is not frozen, it subtracts elapsed wall-clock time since
 * `turnStartedAt` and re-renders every 250ms so the display stays smooth.
 * Server snapshots (received via WebSocket) re-anchor the countdown.
 */
export function PlayerClock({
  remainingMs,
  turnStartedAt,
  isActive,
  frozen,
}: PlayerClockProps) {
  // displayMs is the *rendered* countdown — initialised from the
  // server snapshot, recomputed by the interval tick below using
  // Date.now() so the wall-clock reference stays out of render
  // (React Compiler flags Date.now() during render as impure).
  const [displayMs, setDisplayMs] = useState(remainingMs);
  // Re-seed when the server pushes a fresh snapshot (a new turn starts
  // or remainingMs changes). React's "derived state from props"
  // pattern: compare to the previous value and reset during render.
  const [prevRemainingMs, setPrevRemainingMs] = useState(remainingMs);
  const [prevTurnStartedAt, setPrevTurnStartedAt] = useState(turnStartedAt);
  if (
    prevRemainingMs !== remainingMs ||
    prevTurnStartedAt !== turnStartedAt
  ) {
    setPrevRemainingMs(remainingMs);
    setPrevTurnStartedAt(turnStartedAt);
    setDisplayMs(remainingMs);
  }

  useEffect(() => {
    if (!isActive || frozen || !turnStartedAt) return;
    const turnStart = new Date(turnStartedAt).getTime();
    const tick = () => {
      // Date.now() lives inside the interval callback, not render —
      // the lint rule that flags impure calls during render is
      // satisfied, and the user-visible countdown stays smooth.
      const elapsed = Date.now() - turnStart;
      setDisplayMs(Math.max(0, remainingMs - elapsed));
    };
    tick();
    const id = window.setInterval(tick, 250);
    return () => window.clearInterval(id);
  }, [isActive, frozen, remainingMs, turnStartedAt]);

  const flagged = displayMs <= 0;
  return (
    <span
      className={`inline-block min-w-[3.5rem] rounded px-1.5 py-0.5 text-center font-mono tabular-nums text-sm ${
        flagged
          ? "bg-red-500/20 text-red-300"
          : isActive && !frozen
            ? "bg-amber-400/20 text-amber-200"
            : "bg-zinc-800 text-zinc-300"
      }`}
    >
      {formatMs(displayMs)}
    </span>
  );
}

function formatMs(ms: number): string {
  if (ms <= 0) return "0:00";
  const totalSec = Math.ceil(ms / 1000);
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
