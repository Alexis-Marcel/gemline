import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import type { Game, GameRatings, PlayerScore, Seat } from "../api/types";
import { gemColor } from "../lib/colors";
import { PlayerClock } from "./PlayerClock";

export type SeatCardVariant = "scoreboard" | "strip";

interface SeatCardProps {
  game: Game;
  player: PlayerScore;
  seat: Seat;
  mySeatIndex: number | null;
  myUserId: string | null;
  ratings: GameRatings | null;
  online: boolean | undefined;
  /** Visual style — `scoreboard` is the vertical desktop-rail card
   *  (taller, full stats grid), `strip` is the compact horizontal
   *  mobile card (shorter, inline stats, fixed min-width for the
   *  scroll container). */
  variant: SeatCardVariant;
  onAddBot?: (seatIndex: number) => void;
  onRemoveBot?: (seatIndex: number) => void;
  onInviteSeat?: (seatIndex: number) => void;
  onCancelInvite?: (seatIndex: number) => void;
  onAcceptInvite?: (seatIndex: number) => void;
  onDeclineInvite?: (seatIndex: number) => void;
}

const DISCONNECT_GRACE_MS = 60_000;

/**
 * SeatCard renders one player's tile. Used by both Scoreboard (vertical
 * desktop rail) and PlayerStrip (horizontal mobile strip) — the two
 * containers used to inline ~150 lines of the same per-seat logic each.
 * The shape, badges, action buttons, presence countdown and rating chip
 * all live here; the variant prop tweaks padding, min-width and how the
 * stats row lays out.
 */
export function SeatCard({
  game,
  player,
  seat,
  mySeatIndex,
  myUserId,
  ratings,
  online,
  variant,
  onAddBot,
  onRemoveBot,
  onInviteSeat,
  onCancelInvite,
  onAcceptInvite,
  onDeclineInvite,
}: SeatCardProps) {
  const t = game.thresholds;
  const clockEnabled = t.initialTimeMs > 0;
  const gameOver = game.status === "finished";
  const waiting = game.status === "waiting";
  const isTurn = game.turn === seat.index && game.status === "playing";
  const isYou = mySeatIndex === seat.index;
  const showOffline =
    seat.occupied && game.status === "playing" && online === false;
  // Invited-but-not-joined: server persists a userId + name on the seat
  // but Occupied stays false, IsBot stays false.
  const isInvited =
    !seat.occupied && !seat.isBot && !!seat.userId && seat.name !== "";
  const isMyInvite = isInvited && !!myUserId && seat.userId === myUserId;
  const sr =
    ratings?.rated
      ? ratings.seats.find((s) => s.seatIndex === seat.index)
      : undefined;

  const stripVariant = variant === "strip";
  const dotSize = stripVariant ? "h-3 w-3" : "h-5 w-5";
  const cardCls = stripVariant
    ? // Strip: fixed min-width so the horizontal scroll has stable
      // snap points, tight padding, no responsive backdrop.
      "min-w-[10rem] flex-shrink-0 snap-start rounded-lg border p-2 text-xs transition-colors lg:min-w-[12rem]"
    : // Scoreboard: tighter padding on phones (where it's currently
      // unused after BoardFirst, but the same card is still used by
      // Scoreboard which DesktopGameRail renders).
      "rounded-lg border bg-zinc-950/80 p-2 backdrop-blur transition-colors lg:bg-transparent lg:p-3 lg:backdrop-blur-none";
  const borderCls = isTurn
    ? "border-amber-400/60 bg-amber-400/5 shadow-[inset_3px_0_0_0_rgba(251,191,36,0.9)]"
    : seat.occupied
      ? "border-zinc-800 bg-zinc-900/60"
      : "border-dashed border-zinc-800 bg-zinc-900/30";

  return (
    <li key={player.color} className={`${cardCls} ${borderCls}`}>
      <div
        className={
          stripVariant ? "flex items-center gap-2" : "flex items-center gap-3"
        }
      >
        <span
          aria-hidden
          className={`inline-block ${dotSize} flex-none rounded-full border border-black/40`}
          style={{
            background: seat.occupied
              ? (gemColor(player.color) ?? "#27272a")
              : "transparent",
            borderColor: seat.occupied ? undefined : "#3f3f46",
          }}
        />
        <span
          className={
            stripVariant
              ? "min-w-0 flex-1 truncate font-medium"
              : "min-w-0 flex-1 truncate font-medium"
          }
        >
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
            remainingMs={player.timeRemainingMs}
            turnStartedAt={game.turnStartedAt}
            isActive={isTurn}
            frozen={gameOver}
          />
        )}
      </div>

      {/* Status badge row */}
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
        {seat.occupied && sr && (
          <RatingChip sr={sr} applied={ratings?.applied ?? false} />
        )}
        {showOffline && <DisconnectBadge />}
      </div>

      {/* Stats row — present once playing/finished and the seat is
         actually occupied. The two variants format it differently:
         strip uses a single inline line, scoreboard a two-column
         labelled grid on desktop with a compact mobile fallback. */}
      {!waiting && seat.occupied && (
        <StatsRow
          player={player}
          capturePairsWin={t.capturePairsWin}
          variant={variant}
        />
      )}

      {/* Lobby-only inline actions per seat. */}
      {waiting && seat.occupied && seat.isBot && onRemoveBot && (
        <ActionRow>
          <ActionButton
            danger
            onClick={() => onRemoveBot(seat.index)}
            label="× Retirer le bot"
          />
        </ActionRow>
      )}
      {waiting && isMyInvite && (onAcceptInvite || onDeclineInvite) && (
        <ActionRow split>
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
            <ActionButton
              danger
              onClick={() => onDeclineInvite(seat.index)}
              label="Refuser"
            />
          )}
        </ActionRow>
      )}
      {waiting && isInvited && !isMyInvite && onCancelInvite && (
        <ActionRow>
          <ActionButton
            danger
            onClick={() => onCancelInvite(seat.index)}
            label="× Annuler"
          />
        </ActionRow>
      )}
      {waiting && !seat.occupied && !isInvited && (onAddBot || onInviteSeat) && (
        <ActionRow split>
          {onInviteSeat && (
            <ActionButton
              onClick={() => onInviteSeat(seat.index)}
              label="+ Inviter"
            />
          )}
          {onAddBot && (
            <ActionButton onClick={() => onAddBot(seat.index)} label="+ Bot" />
          )}
        </ActionRow>
      )}
    </li>
  );
}

function StatsRow({
  player,
  capturePairsWin,
  variant,
}: {
  player: PlayerScore;
  capturePairsWin: number;
  variant: SeatCardVariant;
}) {
  // Strip + mobile scoreboard share the single-line format; only
  // desktop scoreboard switches to the labelled grid.
  const inline = (
    <div
      className={
        variant === "strip"
          ? "mt-1 text-[11px] text-zinc-400"
          : "mt-1 text-xs text-zinc-400 lg:hidden"
      }
    >
      <span className="text-zinc-200">
        {player.capturedPairs}/{capturePairsWin}
      </span>{" "}
      paires ·{" "}
      <span className="text-zinc-200">{player.gemsRemaining}</span> gemmes
    </div>
  );
  if (variant === "strip") return inline;
  return (
    <>
      {inline}
      <div className="mt-1 hidden grid-cols-2 gap-1 text-xs text-zinc-400 lg:grid">
        <Stat
          label="Paires"
          value={`${player.capturedPairs}/${capturePairsWin}`}
        />
        <Stat label="Gemmes" value={`${player.gemsRemaining}`} />
      </div>
    </>
  );
}

function ActionRow({
  children,
  split,
}: {
  children: React.ReactNode;
  split?: boolean;
}) {
  return (
    <div className={`mt-2 ${split ? "flex gap-1" : ""}`}>{children}</div>
  );
}

function ActionButton({
  onClick,
  label,
  danger,
}: {
  onClick: () => void;
  label: string;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] transition " +
        (danger
          ? "text-zinc-400 hover:border-red-400 hover:text-red-300"
          : "text-zinc-200 hover:border-amber-400 hover:text-amber-100") +
        " flex-1"
      }
    >
      {label}
    </button>
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
    <span className="inline-flex items-baseline gap-1 font-mono normal-case tabular-nums text-zinc-300">
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
    <span className="rounded bg-red-500/20 px-1.5 py-0.5 font-medium normal-case text-red-300">
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
