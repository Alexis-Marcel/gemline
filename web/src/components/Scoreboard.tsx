import type { Game, GameRatings } from "../api/types";
import { SeatCard } from "./SeatCard";

interface ScoreboardProps {
  game: Game;
  mySeatIndex: number | null;
  // Used to detect "this seat's invitation is for me" for the Accept/Refuse row.
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
