import type { Game } from "../api/types";
import { gemColor, gemName } from "../lib/colors";

interface ScoreboardProps {
  game: Game;
  mySeatIndex: number | null;
}

export function Scoreboard({ game, mySeatIndex }: ScoreboardProps) {
  const t = game.thresholds;
  return (
    <ul className="flex flex-col gap-2">
      {game.players.map((p, i) => {
        const seat = game.seats[i];
        const isTurn = game.turn === i && game.status === "playing";
        const isYou = mySeatIndex === i;
        return (
          <li
            key={p.color}
            className={`rounded-lg border p-3 transition ${
              isTurn
                ? "border-yellow-400 bg-yellow-400/5"
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
                  {isTurn && (
                    <span className="text-xs font-medium text-yellow-400">
                      à jouer
                    </span>
                  )}
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
