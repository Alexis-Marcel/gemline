import type { Game } from "../api/types";
import { Button } from "./Button";

interface RematchControlsProps {
  game: Game;
  /** Seat index of the local user in this finished game, or null if the
   *  viewer was just a spectator (no creds). Drives whether action
   *  buttons render and which state the offer is in for *this* viewer. */
  mySeatIndex: number | null;
  /** True while any rematch action (offer/accept/decline) is in flight. */
  busy: boolean;
  onOffer: () => void;
  onDecline: () => void;
  onGoToRematch: () => void;
}

/**
 * RematchControls renders the chess.com-style rematch state machine:
 *
 *   1. Rematch game already created  →  "Aller à la revanche"
 *   2. No offer yet                   →  "Proposer une revanche"
 *   3. I've accepted, waiting on N    →  "En attente : X, Y" + "Annuler"
 *   4. Someone proposes, I haven't    →  "X propose une revanche" + Accepter/Refuser
 *
 * Spectators (mySeatIndex === null) get the navigation button in case (1)
 * and a read-only status line in (3)/(4); they never see the action buttons
 * since they have no seat to act from.
 */
export function RematchControls({
  game,
  mySeatIndex,
  busy,
  onOffer,
  onDecline,
  onGoToRematch,
}: RematchControlsProps) {
  // (1) The rematch game exists — anyone can jump to it.
  if (game.rematchGameId) {
    return (
      <Button variant="secondary" onClick={onGoToRematch} className="w-full">
        Aller à la revanche
      </Button>
    );
  }

  const offer = game.rematchOffer;
  const isSpectator = mySeatIndex === null;

  // (2) No offer pending — propose one.
  if (!offer) {
    if (isSpectator) {
      // Nothing useful to show: no link, no offer. Render nothing rather
      // than a disabled button — the parent can decide what fills the space.
      return null;
    }
    return (
      <Button
        variant="secondary"
        onClick={onOffer}
        disabled={busy}
        className="w-full"
      >
        {busy ? "Envoi…" : "Proposer une revanche"}
      </Button>
    );
  }

  const iAccepted =
    mySeatIndex !== null && offer.acceptedSeats.includes(mySeatIndex);
  const namesOf = (seats: number[]) =>
    seats
      .map((i) => game.seats[i]?.name)
      .filter((n): n is string => !!n)
      .join(", ");

  // (3) I have already accepted — waiting for the remaining seats.
  if (iAccepted) {
    const pendingNames = namesOf(offer.pendingSeats);
    return (
      <div className="space-y-2 rounded-md border border-zinc-800 bg-zinc-900/40 p-3 text-sm">
        <p className="text-zinc-200">
          Revanche proposée — en attente :{" "}
          <span className="font-medium text-amber-300">
            {pendingNames || "…"}
          </span>
        </p>
        {!isSpectator && (
          <Button
            variant="secondary"
            onClick={onDecline}
            disabled={busy}
            className="w-full"
          >
            Annuler
          </Button>
        )}
      </div>
    );
  }

  // (4) Someone else proposed, I haven't responded yet.
  const proposerNames = namesOf(offer.acceptedSeats);
  return (
    <div className="space-y-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
      <p className="text-zinc-100">
        <span className="font-medium text-amber-300">
          {proposerNames || "Un joueur"}
        </span>{" "}
        propose une revanche
      </p>
      {!isSpectator && (
        <div className="flex gap-2">
          <Button onClick={onOffer} disabled={busy} className="flex-1">
            {busy ? "…" : "Accepter"}
          </Button>
          <Button
            variant="secondary"
            onClick={onDecline}
            disabled={busy}
            className="flex-1"
          >
            Refuser
          </Button>
        </div>
      )}
    </div>
  );
}
