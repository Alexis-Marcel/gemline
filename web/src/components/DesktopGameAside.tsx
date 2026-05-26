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
  /** Per-seat callbacks for the lobby — undefined outside the
   *  (waiting + private + seated) trifecta so the Scoreboard renders
   *  empty cards without action chrome. Mirrors the shape returned by
   *  GamePage's stripCallbacks. */
  seatCallbacks: {
    onAddBot?: (seatIndex: number) => void;
    onRemoveBot?: (seatIndex: number) => void;
    onInviteSeat?: (seatIndex: number) => void;
    onCancelInvite?: (seatIndex: number) => void;
    onAcceptInvite?: (seatIndex: number) => void;
    onDeclineInvite?: (seatIndex: number) => void;
  };
  /** Host-only "Lancer la partie" affordance. Pass undefined to hide
   *  the button (non-host viewers, non-waiting games). */
  onStart?: () => void;
  /** The game id for the share-by-URL card. Only rendered while
   *  the game is waiting + private. */
  gameId: string;
}

/**
 * DesktopGameAside is the left rail of the desktop 3-column layout:
 * scoreboard + lobby actions (StartButton on waiting+host games,
 * ShareCard on waiting games). Hidden on mobile via Tailwind in the
 * caller — this component itself doesn't carry visibility classes.
 */
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
