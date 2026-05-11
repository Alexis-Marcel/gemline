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
  const [, setNow] = useState(0);

  useEffect(() => {
    if (!isActive || frozen) return;
    const id = window.setInterval(() => setNow((n) => n + 1), 250);
    return () => window.clearInterval(id);
  }, [isActive, frozen]);

  let displayMs = remainingMs;
  if (isActive && !frozen && turnStartedAt) {
    const elapsed = Date.now() - new Date(turnStartedAt).getTime();
    displayMs = Math.max(0, remainingMs - elapsed);
  }

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
