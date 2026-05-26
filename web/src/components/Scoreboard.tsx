import type { Game, GameRatings } from "../api/types";
import { SeatCard } from "./SeatCard";

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
   *  each seat surfaces its current Elo as a small chip; otherwise
   *  hidden. */
  ratings?: GameRatings | null;
  onAddBot?: (seatIndex: number) => void;
  onRemoveBot?: (seatIndex: number) => void;
  onInviteSeat?: (seatIndex: number) => void;
  onCancelInvite?: (seatIndex: number) => void;
  onAcceptInvite?: (seatIndex: number) => void;
  onDeclineInvite?: (seatIndex: number) => void;
}

/**
 * Scoreboard is the desktop-rail seat list: one card per seat, stacked
 * vertically on lg+. The card rendering itself lives in SeatCard
 * (shared with PlayerStrip's horizontal mobile variant); this
 * component only owns the container — a flex column on desktop, a
 * sticky 2-col grid on phones for legacy callers.
 */
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
  return (
    <ul className="sticky top-2 z-10 grid grid-cols-2 gap-2 lg:static lg:flex lg:flex-col">
      {game.players.map((p, i) => (
        <SeatCard
          key={p.color}
          variant="scoreboard"
          game={game}
          player={p}
          seat={game.seats[i]}
          mySeatIndex={mySeatIndex}
          myUserId={myUserId}
          ratings={ratings ?? null}
          online={presence[i]}
          onAddBot={onAddBot}
          onRemoveBot={onRemoveBot}
          onInviteSeat={onInviteSeat}
          onCancelInvite={onCancelInvite}
          onAcceptInvite={onAcceptInvite}
          onDeclineInvite={onDeclineInvite}
        />
      ))}
    </ul>
  );
}
