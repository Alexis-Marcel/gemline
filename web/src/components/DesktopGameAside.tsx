import type { Game, GameRatings } from "../api/types";
import { Scoreboard } from "./Scoreboard";
import { ShareCard } from "./ShareCard";
import { StartButton } from "./StartButton";

interface DesktopGameAsideProps {
  game: Game;
  mySeatIndex: number | null;
  myUserId: string | null;
  presence: Record<number, boolean>;
  ratings: GameRatings | null;
  // Per-seat lobby callbacks; undefined when actions don't apply.
  seatCallbacks: {
    onAddBot?: (seatIndex: number) => void;
    onRemoveBot?: (seatIndex: number) => void;
    onInviteSeat?: (seatIndex: number) => void;
    onCancelInvite?: (seatIndex: number) => void;
    onAcceptInvite?: (seatIndex: number) => void;
    onDeclineInvite?: (seatIndex: number) => void;
  };
  // Host-only start affordance; undefined to hide.
  onStart?: () => void;
  gameId: string;
}

export function DesktopGameAside({
  game,
  mySeatIndex,
  myUserId,
  presence,
  ratings,
  seatCallbacks,
  onStart,
  gameId,
}: DesktopGameAsideProps) {
  return (
    <aside className="hidden flex-col gap-3 lg:flex lg:col-start-1 lg:self-start">
      <Scoreboard
        game={game}
        mySeatIndex={mySeatIndex}
        myUserId={myUserId}
        presence={presence}
        ratings={ratings}
        onAddBot={seatCallbacks.onAddBot}
        onRemoveBot={seatCallbacks.onRemoveBot}
        onInviteSeat={seatCallbacks.onInviteSeat}
        onCancelInvite={seatCallbacks.onCancelInvite}
        onAcceptInvite={seatCallbacks.onAcceptInvite}
        onDeclineInvite={seatCallbacks.onDeclineInvite}
      />
      {onStart && <StartButton game={game} onStart={onStart} />}
      {game.status === "waiting" && <ShareCard id={gameId} />}
    </aside>
  );
}
