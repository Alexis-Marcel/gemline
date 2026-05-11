import { useEffect, useState } from "react";
import type { Game } from "../api/types";
import { gemColor, gemName } from "../lib/colors";
import { PlayerClock } from "./PlayerClock";

interface ScoreboardProps {
  game: Game;
  mySeatIndex: number | null;
  /** Per-seat presence flags pushed by the server (key = seatIndex). */
  presence?: Record<number, boolean>;
}

const DISCONNECT_GRACE_MS = 60_000;

export function Scoreboard({ game, mySeatIndex, presence = {} }: ScoreboardProps) {
  const t = game.thresholds;
  const clockEnabled = t.initialTimeMs > 0;
  const gameOver = game.status === "finished";
  return (
    <ul className="grid grid-cols-2 gap-2 lg:flex lg:flex-col">
      {game.players.map((p, i) => {
        const seat = game.seats[i];
        const isTurn = game.turn === i && game.status === "playing";
        const isYou = mySeatIndex === i;
        const online = presence[i];
        // Only show offline state for seats that are actually claimed and
        // when the game is in play (not waiting / finished).
        const showOffline =
          seat.occupied && game.status === "playing" && online === false;
        return (
          <li
            key={p.color}
            className={`rounded-lg border p-3 transition-colors ${
              isTurn
                ? "border-amber-400/60 bg-amber-400/5 shadow-[inset_3px_0_0_0_rgba(251,191,36,0.9)]"
                : "border-zinc-800 bg-zinc-900/50"
            }`}
          >
            <div className="flex items-center gap-3">
              <span
                aria-hidden
                className="inline-block h-5 w-5 rounded-full border border-black/40"
                style={{ background: gemColor(p.color) ?? "#27272a" }}
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="truncate font-medium text-zinc-100">
                    {seat.name || gemName(p.color)}
                    {isYou && (
                      <span className="ml-2 text-xs text-zinc-400">(toi)</span>
                    )}
                  </span>
                  {clockEnabled && (
                    <PlayerClock
                      remainingMs={p.timeRemainingMs}
                      turnStartedAt={game.turnStartedAt}
                      isActive={isTurn}
                      frozen={gameOver}
                    />
                  )}
                </div>
                <div className="mt-0.5 flex items-center gap-2 text-xs">
                  {isTurn && (
                    <span className="font-medium text-amber-400">à jouer</span>
                  )}
                  {showOffline && <DisconnectBadge />}
                </div>
                <div className="mt-1 grid grid-cols-2 gap-1 text-xs text-zinc-400">
                  <Stat
                    label="Paires"
                    value={`${p.capturedPairs}/${t.capturePairsWin}`}
                  />
                  <Stat label="Gemmes" value={`${p.gemsRemaining}`} />
                </div>
              </div>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * DisconnectBadge counts down the disconnect-grace period. We don't get a
 * server-side timestamp for when the seat went offline, so we anchor at
 * mount — i.e. when the client observed the presence flip. Worst case the
 * displayed countdown is slightly off, but it's the right order of magnitude
 * and gives players an honest signal that a forfeit is about to land.
 */
function DisconnectBadge() {
  const [remaining, setRemaining] = useState(DISCONNECT_GRACE_MS);
  useEffect(() => {
    const start = Date.now();
    const id = window.setInterval(() => {
      setRemaining(Math.max(0, DISCONNECT_GRACE_MS - (Date.now() - start)));
    }, 500);
    return () => window.clearInterval(id);
  }, []);
  const seconds = Math.ceil(remaining / 1000);
  return (
    <span className="rounded bg-red-500/20 px-1.5 py-0.5 font-medium text-red-300">
      hors-ligne {seconds > 0 ? `· ${seconds}s` : "· forfait"}
    </span>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <span className="block text-[0.65rem] uppercase tracking-wide text-zinc-500">
        {label}
      </span>
      <span className="text-zinc-200">{value}</span>
    </div>
  );
}
