import { useEffect, useState } from "react";

interface PlayerClockProps {
  // Server-reported time remaining in ms (frozen at the last snapshot).
  remainingMs: number;
  // ISO turn-start timestamp, or undefined if not their turn / no clock.
  turnStartedAt: string | undefined;
  isActive: boolean;
  frozen: boolean;
}

export function PlayerClock({
  remainingMs,
  turnStartedAt,
  isActive,
  frozen,
}: PlayerClockProps) {
  const [displayMs, setDisplayMs] = useState(remainingMs);
  // Re-seed display from a fresh server snapshot via the derived-state pattern
  // (compare to previous and reset during render).
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
      // Date.now() must stay out of render (flagged as impure); keep it here.
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
