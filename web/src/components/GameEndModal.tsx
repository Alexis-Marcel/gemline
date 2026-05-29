import { useEffect } from "react";
import { Link } from "react-router-dom";
import type { Game, GameRatings, WinKind } from "../api/types";
import { gemColor, gemName } from "../lib/colors";
import { Button } from "./Button";
import { RematchControls } from "./RematchControls";

interface GameEndModalProps {
  game: Game;
  // null while loading; rated=false hides the Elo block; applied=false means
  // the server is still crunching deltas (shows "calcul en cours").
  ratings: GameRatings | null;
  // Seat index of the local user, or null for spectators.
  mySeatIndex: number | null;
  rematching: boolean;
  newGameBusy: boolean;
  newGameBusyLabel: string | null;
  onOfferRematch: () => void;
  onDeclineRematch: () => void;
  onGoToRematch: () => void;
  onNewGame: (() => void) | null;
  onClose: () => void;
  onLeave: () => void;
}

export function GameEndModal({
  game,
  ratings,
  mySeatIndex,
  rematching,
  newGameBusy,
  newGameBusyLabel,
  onOfferRematch,
  onDeclineRematch,
  onGoToRematch,
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
                    {seat.userId && !seat.isBot ? (
                      <Link
                        to={`/profile/${seat.userId}`}
                        onClick={onClose}
                        className="flex-1 truncate text-sm text-zinc-100 hover:text-amber-300 hover:underline"
                      >
                        {seat.name}
                      </Link>
                    ) : (
                      <span className="flex-1 truncate text-sm text-zinc-100">
                        {seat.name}
                      </span>
                    )}
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
              disabled={newGameBusy}
              className="w-full"
            >
              {newGameBusy && newGameBusyLabel
                ? newGameBusyLabel
                : "Nouvelle partie"}
            </Button>
          )}
          <RematchControls
            game={game}
            mySeatIndex={mySeatIndex}
            busy={rematching}
            onOffer={onOfferRematch}
            onDecline={onDeclineRematch}
            onGoToRematch={onGoToRematch}
          />
          <Button variant="secondary" onClick={onLeave} className="w-full">
            Quitter
          </Button>
        </footer>
      </div>
    </div>
  );
}

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
