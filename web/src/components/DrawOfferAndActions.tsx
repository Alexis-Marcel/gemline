import type { Game } from "../api/types";
import { gemName } from "../lib/colors";

interface DrawOfferAndActionsProps {
  game: Game;
  mySeatIndex: number;
  onOfferDraw: () => void;
  onAcceptDraw: () => void;
  onDeclineDraw: () => void;
  onResign: () => void;
}

// Draw controls only show for 2-player games; the server rejects draws for N≠2.
export function DrawOfferAndActions({
  game,
  mySeatIndex,
  onOfferDraw,
  onAcceptDraw,
  onDeclineDraw,
  onResign,
}: DrawOfferAndActionsProps) {
  const drawSupported = game.seats.length === 2;
  const offeredBy = game.drawOfferBy ?? -1;
  const offerPendingByMe = offeredBy === mySeatIndex;
  const offerPendingByThem = offeredBy >= 0 && !offerPendingByMe;

  return (
    <div className="space-y-2">
      {offerPendingByThem && (
        <div className="space-y-2 rounded-xl border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-100">
          <div>
            🤝 {gemName(game.seats[offeredBy]?.color ?? 0)} propose un nul.
          </div>
          <div className="flex gap-2">
            <button
              onClick={onAcceptDraw}
              className="flex-1 rounded-md bg-amber-400 px-3 py-1.5 text-xs font-medium text-zinc-950 transition hover:bg-amber-300"
            >
              Accepter
            </button>
            <button
              onClick={onDeclineDraw}
              className="flex-1 rounded-md border border-amber-400/50 px-3 py-1.5 text-xs text-amber-100 transition hover:bg-amber-400/10"
            >
              Refuser
            </button>
          </div>
        </div>
      )}

      {offerPendingByMe && (
        <div className="flex items-center justify-between rounded-md border border-zinc-700 bg-zinc-900/60 p-2 text-xs text-zinc-300">
          <span>En attente de l'adversaire pour le nul…</span>
          <button
            onClick={onDeclineDraw}
            className="text-zinc-400 underline-offset-2 hover:text-zinc-200 hover:underline"
          >
            Retirer
          </button>
        </div>
      )}

      <div className="flex gap-2">
        {drawSupported && offeredBy < 0 && (
          <button
            onClick={onOfferDraw}
            className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-xs text-zinc-200 transition hover:border-zinc-500"
          >
            Proposer un nul
          </button>
        )}
        <button
          onClick={onResign}
          className="flex-1 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-xs text-red-200 transition hover:border-red-700"
        >
          Abandonner
        </button>
      </div>
    </div>
  );
}
