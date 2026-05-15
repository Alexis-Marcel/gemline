import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import type { Game, GameRatings } from "../api/types";
import { gemColor } from "../lib/colors";
import { PlayerClock } from "./PlayerClock";

interface ScoreboardProps {
  game: Game;
  mySeatIndex: number | null;
  /** Supabase user id of the local viewer, or null if anonymous. Used to
   *  detect "this seat's invitation is for me" so the Accept/Refuse
   *  buttons render on the invitee's own row. */
  myUserId: string | null;
  /** Per-seat presence flags pushed by the server (key = seatIndex). */
  presence?: Record<number, boolean>;
  /** Live ratings snapshot for the game. When ratings.rated is true,
   *  the scoreboard surfaces each seat's current Elo as a small line
   *  under the name. Null/rated:false hides the Elo entirely. */
  ratings?: GameRatings | null;
  /** Invoked when the user clicks the "+ Bot" button on an empty seat.
   *  Only surfaced for private games in waiting state — set to undefined
   *  for any other context (public games, playing, finished). */
  onAddBot?: (seatIndex: number) => void;
  /** Invoked when the user clicks the "× Bot" button on a bot-occupied
   *  seat. Same conditions as onAddBot (private + waiting); undefined
   *  in any other context. */
  onRemoveBot?: (seatIndex: number) => void;
  /** Invoked when the user clicks "+ Inviter" on an empty seat. Same
   *  conditions as onAddBot. */
  onInviteSeat?: (seatIndex: number) => void;
  /** Invoked when the user (host) clicks "× Annuler" on a seat that's
   *  reserved-but-not-joined. Same conditions as onAddBot. */
  onCancelInvite?: (seatIndex: number) => void;
  /** Invoked when the invited user clicks "Accepter" on their own
   *  reservation. The accept path goes through joinGame on the server
   *  side (pickSeatForUser routes to the reserved seat). */
  onAcceptInvite?: (seatIndex: number) => void;
  /** Invoked when the invited user clicks "Refuser" on their own
   *  reservation. Frees the seat. */
  onDeclineInvite?: (seatIndex: number) => void;
}

const DISCONNECT_GRACE_MS = 60_000;

// Scoreboard renders one row per seat. The same component drives the
// waiting-room layout (showing "Siège vide" + "+ Bot" controls) and the
// in-play scoreboard (showing colour, name, clock, paires/gemmes). This
// keeps the seat metadata in a single place so the user doesn't see a
// duplicated list before play starts.
export function Scoreboard({
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
}: ScoreboardProps) {
  const t = game.thresholds;
  const clockEnabled = t.initialTimeMs > 0;
  const gameOver = game.status === "finished";
  const waiting = game.status === "waiting";
  // Index ratings by seatIndex so the loop below stays O(seats) instead
  // of O(seats × ratings.seats).
  const ratingsBySeat = new Map(
    ratings?.rated ? ratings.seats.map((s) => [s.seatIndex, s]) : [],
  );
  return (
    <ul className="grid grid-cols-2 gap-2 lg:flex lg:flex-col">
      {game.players.map((p, i) => {
        const seat = game.seats[i];
        const isTurn = game.turn === i && game.status === "playing";
        const isYou = mySeatIndex === i;
        const online = presence[i];
        const showOffline =
          seat.occupied && game.status === "playing" && online === false;
        // A seat is "invited but not joined" when it has a userId and a
        // display name set, but Occupied is false and it's not a bot —
        // exactly what Store.InviteSeat persists. The UI shows the
        // invitee's name with an "en attente" badge until they actually
        // navigate to the URL and Store.Join lands them on this exact
        // seat.
        const isInvited =
          !seat.occupied && !seat.isBot && !!seat.userId && seat.name !== "";
        // "Cette invitation me concerne" — only the matching user sees
        // Accepter / Refuser; everyone else sees the host's "× Annuler"
        // affordance (when they hold the host capability).
        const isMyInvite = isInvited && !!myUserId && seat.userId === myUserId;
        return (
          <li
            key={p.color}
            className={`rounded-lg border p-3 transition-colors ${
              isTurn
                ? "border-amber-400/60 bg-amber-400/5 shadow-[inset_3px_0_0_0_rgba(251,191,36,0.9)]"
                : seat.occupied
                  ? "border-zinc-800 bg-zinc-900/50"
                  : "border-dashed border-zinc-800 bg-zinc-900/30"
            }`}
          >
            <div className="flex items-center gap-3">
              <span
                aria-hidden
                className="inline-block h-5 w-5 rounded-full border border-black/40"
                style={{
                  background: seat.occupied
                    ? (gemColor(p.color) ?? "#27272a")
                    : "transparent",
                  borderColor: seat.occupied ? undefined : "#3f3f46",
                }}
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="truncate font-medium">
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
                    {seat.isBot && (
                      <span className="ml-2 rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-zinc-400">
                        Bot
                      </span>
                    )}
                    {isInvited && (
                      <span className="ml-2 rounded bg-amber-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-amber-300">
                        En attente
                      </span>
                    )}
                    {isYou && (
                      <span className="ml-2 text-xs text-amber-300">(toi)</span>
                    )}
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
                <div className="mt-0.5 flex items-center gap-2 text-xs">
                  {isTurn && (
                    <span className="font-medium text-amber-400">à jouer</span>
                  )}
                  {seat.occupied && ratingsBySeat.has(i) && (
                    <RatingChip sr={ratingsBySeat.get(i)!} applied={ratings?.applied ?? false} />
                  )}
                  {showOffline && <DisconnectBadge />}
                </div>
                {!waiting && seat.occupied && (
                  <div className="mt-1 grid grid-cols-2 gap-1 text-xs text-zinc-400">
                    <Stat
                      label="Paires"
                      value={`${p.capturedPairs}/${t.capturePairsWin}`}
                    />
                    <Stat label="Gemmes" value={`${p.gemsRemaining}`} />
                  </div>
                )}
                {waiting && seat.occupied && seat.isBot && onRemoveBot && (
                  <div className="mt-2">
                    <button
                      type="button"
                      onClick={() => onRemoveBot(seat.index)}
                      className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-400 transition hover:border-red-400 hover:text-red-300"
                    >
                      × Retirer le bot
                    </button>
                  </div>
                )}
                {waiting && isMyInvite && (onAcceptInvite || onDeclineInvite) && (
                  <div className="mt-2 flex gap-2">
                    {onAcceptInvite && (
                      <button
                        type="button"
                        onClick={() => onAcceptInvite(seat.index)}
                        className="rounded-md bg-amber-400 px-2 py-1 text-xs font-medium text-zinc-950 transition hover:bg-amber-300"
                      >
                        Accepter
                      </button>
                    )}
                    {onDeclineInvite && (
                      <button
                        type="button"
                        onClick={() => onDeclineInvite(seat.index)}
                        className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-400 transition hover:border-red-400 hover:text-red-300"
                      >
                        Refuser
                      </button>
                    )}
                  </div>
                )}
                {waiting && isInvited && !isMyInvite && onCancelInvite && (
                  <div className="mt-2">
                    <button
                      type="button"
                      onClick={() => onCancelInvite(seat.index)}
                      className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-400 transition hover:border-red-400 hover:text-red-300"
                    >
                      × Annuler l'invitation
                    </button>
                  </div>
                )}
                {waiting && !seat.occupied && !isInvited && (onAddBot || onInviteSeat) && (
                  <div className="mt-2 flex gap-2">
                    {onInviteSeat && (
                      <button
                        type="button"
                        onClick={() => onInviteSeat(seat.index)}
                        className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-200 transition hover:border-amber-400 hover:text-amber-100"
                      >
                        + Inviter
                      </button>
                    )}
                    {onAddBot && (
                      <button
                        type="button"
                        onClick={() => onAddBot(seat.index)}
                        className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-200 transition hover:border-amber-400 hover:text-amber-100"
                      >
                        + Bot
                      </button>
                    )}
                  </div>
                )}
              </div>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * RatingChip renders a player's current rating and (once the game's
 * Elo math has been applied) the delta they took out of this match.
 * No label: the number sits beside "à jouer" / clock / paires which
 * already give it context, and the colored delta makes the meaning
 * unambiguous after the game ends.
 */
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
    <span className="inline-flex items-baseline gap-1 font-mono tabular-nums text-zinc-300">
      {sr.currentRating}
      {delta !== undefined && (
        <span className={`text-xs font-semibold ${deltaCls}`}>
          {delta > 0 ? `+${delta}` : delta < 0 ? `${delta}` : "±0"}
        </span>
      )}
    </span>
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
