import type { Thresholds } from "../api/types";

/**
 * Objectives summarises the win conditions for the running game. We
 * intentionally display only what the rulebook promises; the live count of
 * alignments for each player is hidden — counting your own and your
 * opponents' lines is part of the game.
 */
export function Objectives({ thresholds }: { thresholds: Thresholds }) {
  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-3">
      <h2 className="mb-2 text-sm font-medium text-zinc-200">
        Conditions de victoire
      </h2>
      <ul className="space-y-1.5 text-xs text-zinc-400">
        <li className="flex items-baseline gap-2">
          <Token>×1</Token>
          <span>alignement de 6</span>
        </li>
        {thresholds.align5ToWin > 0 && (
          <li className="flex items-baseline gap-2">
            <Token>{`×${thresholds.align5ToWin}`}</Token>
            <span>
              alignement{thresholds.align5ToWin > 1 ? "s" : ""} de 5
            </span>
          </li>
        )}
        {thresholds.align4ToWin > 0 && (
          <li className="flex items-baseline gap-2">
            <Token>{`×${thresholds.align4ToWin}`}</Token>
            <span>
              alignement{thresholds.align4ToWin > 1 ? "s" : ""} de 4
            </span>
          </li>
        )}
        {thresholds.capturePairsWin > 0 && (
          <li className="flex items-baseline gap-2">
            <Token>{`×${thresholds.capturePairsWin}`}</Token>
            <span>
              paire{thresholds.capturePairsWin > 1 ? "s" : ""} capturée
              {thresholds.capturePairsWin > 1 ? "s" : ""}
            </span>
          </li>
        )}
      </ul>
      {thresholds.initialTimeMs > 0 && (
        <p className="mt-2 text-[0.7rem] text-zinc-500">
          Horloge : {formatBudget(thresholds.initialTimeMs)} par joueur
          {thresholds.incrementMs > 0 &&
            ` (+${Math.round(thresholds.incrementMs / 1000)}s/coup)`}
          . Drapeau tombé = défaite.
        </p>
      )}
    </section>
  );
}

function formatBudget(ms: number): string {
  const minutes = Math.round(ms / 60_000);
  if (minutes >= 1) return `${minutes} min`;
  return `${Math.round(ms / 1000)} s`;
}

function Token({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex min-w-[1.75rem] justify-center rounded bg-zinc-800 px-1.5 py-0.5 text-[0.7rem] font-medium text-zinc-200">
      {children}
    </span>
  );
}
