import { useEffect } from "react";
import type { Game, GameRatings, WinKind } from "../api/types";
import { gemColor, gemName } from "../lib/colors";
import { Button } from "./Button";

interface GameEndModalProps {
  game: Game;
  /** Rating snapshot fetched/streamed from /api/games/:id/ratings. Null while
   *  loading; rated=false hides the Elo block; applied=false shows
   *  "calcul du rating en cours…" while the server crunches deltas. */
  ratings: GameRatings | null;
  rematchLink: string | null;
  rematching: boolean;
  /** True while the matchmaking queue is being entered or the user is
   *  waiting for a match. The "Nouvelle partie" button shows
   *  "Recherche…" and disables to prevent double-enqueue. */
  matchmakeBusy: boolean;
  onRematch: () => void;
  onNewGame: (() => void) | null;
  onClose: () => void;
  onLeave: () => void;
}

/**
 * GameEndModal takes over the screen on game.status === "finished".
 * Two halves:
 *   - top: winner + win kind, in big letters
 *   - bottom (only when ratings.rated): per-player Elo line. Before the
 *     server's "rated" event arrives, this shows currentRating only and
 *     a "calcul du rating en cours…" hint; once applied, it swaps to the
 *     old → new + delta layout.
 *
 * Closable by clicking the backdrop, pressing Escape, or hitting the X.
 * The parent decides whether to re-open (we don't — once dismissed the
 * banner-less game stays accessible behind for replay / chat).
 */
export function GameEndModal({
  game,
  ratings,
  rematchLink,
  rematching,
  matchmakeBusy,
  onRematch,
  onNewGame,
  onClose,
  onLeave,
}: GameEndModalProps) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const isDraw = game.winKind === 7;
  const ratingsBySeat = new Map(ratings?.seats.map((s) => [s.seatIndex, s]) ?? []);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4"
      onClick={onClose}
    >
      <div
        className="relative w-full max-w-md rounded-2xl border border-zinc-800 bg-zinc-950 p-6 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <button
          type="button"
          onClick={onClose}
          className="absolute right-3 top-3 text-zinc-500 hover:text-zinc-200"
          aria-label="Fermer"
        >
          ✕
        </button>

        <header className="text-center">
          {isDraw ? (
            <>
              <div className="text-5xl">🤝</div>
              <h2 className="mt-3 text-2xl font-semibold text-zinc-100">
                Match nul
              </h2>
              <p className="mt-1 text-sm text-zinc-400">par accord mutuel</p>
            </>
          ) : (
            <>
              <div className="text-5xl">🏆</div>
              <h2 className="mt-3 text-2xl font-semibold text-zinc-100">
                {gemName(game.winner)} gagne
              </h2>
              <p className="mt-1 text-sm text-zinc-400">
                par {winKindLabel(game.winKind)}
              </p>
            </>
          )}
        </header>

        {ratings?.rated && (
          <section className="mt-6 space-y-2">
            <div className="flex items-center justify-between text-xs uppercase tracking-wider text-zinc-500">
              <span>Classement {ratings.mode === "1v1" ? "1v1" : "multi"}</span>
              {!ratings.applied && (
                <span className="text-amber-400">calcul en cours…</span>
              )}
            </div>
            <ul className="space-y-1.5">
              {game.seats.map((seat, i) => {
                const sr = ratingsBySeat.get(i);
                return (
                  <li
                    key={i}
                    className="flex items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 px-3 py-2"
                  >
                    <span
                      aria-hidden
                      className="inline-block h-4 w-4 rounded-full"
                      style={{ background: gemColor(seat.color) ?? "#27272a" }}
                    />
                    <span className="flex-1 truncate text-sm text-zinc-100">
                      {seat.name}
                    </span>
                    <RatingCell sr={sr} />
                  </li>
                );
              })}
            </ul>
          </section>
        )}

        <footer className="mt-6 space-y-2">
          {onNewGame && (
            <Button
              onClick={onNewGame}
              disabled={matchmakeBusy}
              className="w-full"
            >
              {matchmakeBusy ? "Recherche…" : "Nouvelle partie"}
            </Button>
          )}
          <div className="flex gap-2">
            <Button
              variant="secondary"
              onClick={onRematch}
              disabled={rematching}
              className="flex-1"
            >
              {rematching
                ? "Création…"
                : rematchLink
                  ? "Aller à la revanche"
                  : "Revanche"}
            </Button>
            <Button
              variant="secondary"
              onClick={onLeave}
              className="flex-1"
            >
              Quitter
            </Button>
          </div>
        </footer>
      </div>
    </div>
  );
}

/**
 * RatingCell renders the per-seat Elo info in the modal's player list.
 * Three modes:
 *   - no rating row at all (sr undefined): "—" (game wasn't rated for
 *     this seat — shouldn't happen if ratings.rated is true, but cheap
 *     guard)
 *   - rated but not applied yet: just the current rating, dimmed
 *   - applied: oldRating → newRating + colored delta
 */
function RatingCell({ sr }: { sr: SeatRatingMaybe }) {
  if (!sr) return <span className="text-zinc-500">—</span>;
  if (sr.oldRating === undefined || sr.delta === undefined) {
    return <span className="text-zinc-400">{sr.currentRating}</span>;
  }
  const positive = sr.delta > 0;
  const negative = sr.delta < 0;
  const deltaCls = positive
    ? "text-emerald-400"
    : negative
      ? "text-red-400"
      : "text-zinc-400";
  return (
    <span className="flex items-baseline gap-2 font-mono text-sm tabular-nums">
      <span className="text-zinc-500">{sr.oldRating}</span>
      <span className="text-zinc-600">→</span>
      <span className="text-zinc-100">{sr.newRating}</span>
      <span className={`text-xs font-semibold ${deltaCls}`}>
        {sr.delta > 0 ? `+${sr.delta}` : sr.delta}
      </span>
    </span>
  );
}

// Local alias to keep RatingCell signature tight without exposing the
// import surface. The actual type lives in api/types.ts.
type SeatRatingMaybe = NonNullable<GameRatings["seats"][number]> | undefined;

function winKindLabel(k: WinKind): string {
  switch (k) {
    case 1:
      return "alignement de 6";
    case 2:
      return "alignements de 5";
    case 3:
      return "alignements de 4";
    case 4:
      return "captures";
    case 5:
      return "drapeau (temps écoulé)";
    case 6:
      return "forfait";
    case 7:
      return "accord";
    default:
      return "?";
  }
}
