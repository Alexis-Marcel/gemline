import type { Game, GameRatings } from "../api/types";
import { SeatCard } from "./SeatCard";

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

/**
 * PlayerStrip is the mobile-rail seat list: one compact card per seat,
 * laid out horizontally with snap-scroll. The card rendering itself
 * lives in SeatCard (shared with Scoreboard's vertical desktop
 * variant); this component only owns the container.
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
  return (
    <ul className="-mx-2 flex snap-x snap-mandatory gap-2 overflow-x-auto px-2 pb-1 pt-1 lg:mx-0 lg:px-0">
      {game.players.map((p, i) => (
        <SeatCard
          key={p.color}
          variant="strip"
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
