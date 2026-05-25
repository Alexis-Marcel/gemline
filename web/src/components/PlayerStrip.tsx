import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import type { Game, GameRatings } from "../api/types";
import { gemColor } from "../lib/colors";
import { PlayerClock } from "./PlayerClock";

interface PlayerStripProps {
  game: Game;
  mySeatIndex: number | null;
  myUserId: string | null;
  presence?: Record<number, boolean>;
  ratings?: GameRatings | null;
  onAddBot?: (seatIndex: number) => void;
  onRemoveBot?: (seatIndex: number) => void;
  onInviteSeat?: (seatIndex: number) => void;
  onCancelInvite?: (seatIndex: number) => void;
  onAcceptInvite?: (seatIndex: number) => void;
  onDeclineInvite?: (seatIndex: number) => void;
}

const DISCONNECT_GRACE_MS = 60_000;

/**
 * PlayerStrip is the BoardFirst-era replacement for the Scoreboard:
 * a horizontal strip of seat cards that scrolls when there are more
 * seats than fit. Same per-seat surface as before — colour dot, name,
 * clock, captures, gem stock, presence — just laid out side-by-side
 * with a tighter card so the board can take the rest of the viewport.
 *
 * Waiting-state cards keep the +Inviter / +Bot buttons inline so the
 * host can fill seats without leaving the game view. The Lancer la
 * partie button doesn't live here anymore (it sits in the bottom-bar
 * kebab menu) — the strip is only about player identity.
 */
export function PlayerStrip({
  game,
  mySeatIndex,
  myUserId,
  presence = {},
  ratings,
  onAddBot,
  onRemoveBot,
  onInviteSeat,
  onCancelInvite,
  onAcceptInvite,
  onDeclineInvite,
}: PlayerStripProps) {
  const t = game.thresholds;
  const clockEnabled = t.initialTimeMs > 0;
  const gameOver = game.status === "finished";
  const waiting = game.status === "waiting";
  const ratingsBySeat = new Map(
    ratings?.rated ? ratings.seats.map((s) => [s.seatIndex, s]) : [],
  );

  return (
    <ul className="-mx-2 flex snap-x snap-mandatory gap-2 overflow-x-auto px-2 pb-1 pt-1 lg:mx-0 lg:px-0">
      {game.players.map((p, i) => {
        const seat = game.seats[i];
        const isTurn = game.turn === i && game.status === "playing";
        const isYou = mySeatIndex === i;
        const online = presence[i];
        const showOffline =
          seat.occupied && game.status === "playing" && online === false;
        const isInvited =
          !seat.occupied && !seat.isBot && !!seat.userId && seat.name !== "";
        const isMyInvite =
          isInvited && !!myUserId && seat.userId === myUserId;

        return (
          <li
            key={p.color}
            className={
              "min-w-[10rem] flex-shrink-0 snap-start rounded-lg border p-2 text-xs transition-colors lg:min-w-[12rem] " +
              (isTurn
                ? "border-amber-400/60 bg-amber-400/5 shadow-[inset_3px_0_0_0_rgba(251,191,36,0.9)]"
                : seat.occupied
                  ? "border-zinc-800 bg-zinc-900/60"
                  : "border-dashed border-zinc-800 bg-zinc-900/30")
            }
          >
            <div className="flex items-center gap-2">
              <span
                aria-hidden
                className="inline-block h-3 w-3 flex-none rounded-full border border-black/40"
                style={{
                  background: seat.occupied
                    ? (gemColor(p.color) ?? "#27272a")
                    : "transparent",
                  borderColor: seat.occupied ? undefined : "#3f3f46",
                }}
              />
              <span className="min-w-0 flex-1 truncate font-medium">
                {seat.occupied ? (
                  seat.userId && !seat.isBot ? (
                    <Link
                      to={`/profile/${seat.userId}`}
                      className="text-zinc-100 hover:text-amber-300 hover:underline"
                    >
                      {seat.name}
                    </Link>
                  ) : (
                    <span className="text-zinc-100">{seat.name}</span>
                  )
                ) : isInvited ? (
                  <Link
                    to={`/profile/${seat.userId}`}
                    className="text-zinc-300 hover:text-amber-300 hover:underline"
                  >
                    {seat.name}
                  </Link>
                ) : (
                  <span className="text-zinc-500">Siège vide</span>
                )}
                {isYou && <span className="ml-1 text-amber-300">(toi)</span>}
              </span>
              {clockEnabled && seat.occupied && !waiting && (
                <PlayerClock
                  remainingMs={p.timeRemainingMs}
                  turnStartedAt={game.turnStartedAt}
                  isActive={isTurn}
                  frozen={gameOver}
                />
              )}
            </div>

            {/* Status badges row — kept tight; sometimes empty. */}
            <div className="mt-1 flex flex-wrap items-center gap-1 text-[10px] uppercase tracking-wider">
              {isTurn && (
                <span className="font-semibold text-amber-400">à jouer</span>
              )}
              {seat.isBot && (
                <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-zinc-400">
                  Bot
                </span>
              )}
              {isInvited && (
                <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-300">
                  En attente
                </span>
              )}
              {seat.occupied && ratingsBySeat.has(i) && (
                <RatingChip
                  sr={ratingsBySeat.get(i)!}
                  applied={ratings?.applied ?? false}
                />
              )}
              {showOffline && <DisconnectBadge />}
            </div>

            {!waiting && seat.occupied && (
              <div className="mt-1 text-[11px] text-zinc-400">
                <span className="text-zinc-200">
                  {p.capturedPairs}/{t.capturePairsWin}
                </span>{" "}
                paires ·{" "}
                <span className="text-zinc-200">{p.gemsRemaining}</span> gemmes
              </div>
            )}

            {/* Lobby-only inline actions per card. The host fills seats
                from here directly so they don't have to leave the
                player strip. */}
            {waiting && seat.occupied && seat.isBot && onRemoveBot && (
              <button
                type="button"
                onClick={() => onRemoveBot(seat.index)}
                className="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-400 transition hover:border-red-400 hover:text-red-300"
              >
                × Retirer le bot
              </button>
            )}
            {waiting &&
              isMyInvite &&
              (onAcceptInvite || onDeclineInvite) && (
                <div className="mt-1 flex gap-1">
                  {onAcceptInvite && (
                    <button
                      type="button"
                      onClick={() => onAcceptInvite(seat.index)}
                      className="flex-1 rounded-md bg-amber-400 px-2 py-1 text-[11px] font-medium text-zinc-950 transition hover:bg-amber-300"
                    >
                      Accepter
                    </button>
                  )}
                  {onDeclineInvite && (
                    <button
                      type="button"
                      onClick={() => onDeclineInvite(seat.index)}
                      className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-400 transition hover:border-red-400 hover:text-red-300"
                    >
                      Refuser
                    </button>
                  )}
                </div>
              )}
            {waiting && isInvited && !isMyInvite && onCancelInvite && (
              <button
                type="button"
                onClick={() => onCancelInvite(seat.index)}
                className="mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-400 transition hover:border-red-400 hover:text-red-300"
              >
                × Annuler
              </button>
            )}
            {waiting &&
              !seat.occupied &&
              !isInvited &&
              (onAddBot || onInviteSeat) && (
                <div className="mt-1 flex gap-1">
                  {onInviteSeat && (
                    <button
                      type="button"
                      onClick={() => onInviteSeat(seat.index)}
                      className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 transition hover:border-amber-400 hover:text-amber-100"
                    >
                      + Inviter
                    </button>
                  )}
                  {onAddBot && (
                    <button
                      type="button"
                      onClick={() => onAddBot(seat.index)}
                      className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 transition hover:border-amber-400 hover:text-amber-100"
                    >
                      + Bot
                    </button>
                  )}
                </div>
              )}
          </li>
        );
      })}
    </ul>
  );
}

function RatingChip({
  sr,
  applied,
}: {
  sr: { currentRating: number; delta?: number };
  applied: boolean;
}) {
  const delta = applied ? sr.delta ?? 0 : undefined;
  const deltaCls =
    delta === undefined
      ? ""
      : delta > 0
        ? "text-emerald-400"
        : delta < 0
          ? "text-red-400"
          : "text-zinc-500";
  return (
    <span className="inline-flex items-baseline gap-1 font-mono tabular-nums text-zinc-300 normal-case">
      {sr.currentRating}
      {delta !== undefined && (
        <span className={`text-[10px] font-semibold ${deltaCls}`}>
          {delta > 0 ? `+${delta}` : delta < 0 ? `${delta}` : "±0"}
        </span>
      )}
    </span>
  );
}

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
    <span className="rounded bg-red-500/20 px-1.5 py-0.5 font-medium text-red-300 normal-case">
      hors-ligne {seconds > 0 ? `· ${seconds}s` : "· forfait"}
    </span>
  );
}
