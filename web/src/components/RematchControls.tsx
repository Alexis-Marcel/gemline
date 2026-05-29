import type { Game } from "../api/types";
import { Button } from "./Button";

interface RematchControlsProps {
  game: Game;
  // Seat index of the local user, or null for spectators (no seat to act from).
  mySeatIndex: number | null;
  busy: boolean;
  onOffer: () => void;
  onDecline: () => void;
  onGoToRematch: () => void;
}

// Rematch state machine: (1) jump to created game, (2) propose, (3) accepted
// and waiting, (4) someone proposed. Spectators only see read-only states.
export function RematchControls({
  game,
  mySeatIndex,
  busy,
  onOffer,
  onDecline,
  onGoToRematch,
}: RematchControlsProps) {
  // (1) rematch game exists — anyone can jump to it.
  if (game.rematchGameId) {
    return (
      <Button variant="secondary" onClick={onGoToRematch} className="w-full">
        Aller à la revanche
      </Button>
    );
  }

  const offer = game.rematchOffer;
  const isSpectator = mySeatIndex === null;

  // (2) no offer pending — propose one.
  if (!offer) {
    if (isSpectator) {
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

  // (3) already accepted — waiting for the remaining seats.
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

  // (4) someone else proposed, not yet responded.
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
