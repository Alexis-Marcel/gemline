import type { Game } from "../api/types";
import { ChatPanel } from "./ChatPanel";
import { DrawOfferAndActions } from "./DrawOfferAndActions";
import { Objectives } from "./Objectives";
import { RematchControls } from "./RematchControls";
import { ReplayNav } from "./ReplayNav";

interface DesktopGameRailProps {
  game: Game;
  gameId: string;
  // Seat index of the local viewer, or null for spectators.
  mySeatIndex: number | null;
  // Seat token for chat input; null for spectators (read-only).
  playerToken: string | null;
  // In-play action handlers
  onOfferDraw: () => void;
  onAcceptDraw: () => void;
  onDeclineDraw: () => void;
  onResign: () => void;
  // End-game action handlers
  onNewGame: (() => void) | null;
  newGameBusy: boolean;
  creatingNew: boolean;
  onOfferRematch: () => void;
  onDeclineRematch: () => void;
  onGoToRematch: () => void;
  rematching: boolean;
  // Replay nav
  totalMoves: number;
  step: number;
  inReplay: boolean;
  onStep: (step: number) => void;
  openReplay: () => void;
  exitReplay: () => void;
  // Misc
  onLeave: () => void;
  error: string | null;
}

export function DesktopGameRail({
  game,
  gameId,
  mySeatIndex,
  playerToken,
  onOfferDraw,
  onAcceptDraw,
  onDeclineDraw,
  onResign,
  onNewGame,
  newGameBusy,
  creatingNew,
  onOfferRematch,
  onDeclineRematch,
  onGoToRematch,
  rematching,
  totalMoves,
  step,
  inReplay,
  onStep,
  openReplay,
  exitReplay,
  onLeave,
  error,
}: DesktopGameRailProps) {
  const playing = game.status === "playing";
  const finished = game.status === "finished";

  return (
    <aside className="hidden flex-col gap-3 lg:flex lg:col-start-3 lg:self-start">
      <Objectives thresholds={game.thresholds} />

      {playing && mySeatIndex !== null && (
        <DrawOfferAndActions
          game={game}
          mySeatIndex={mySeatIndex}
          onOfferDraw={onOfferDraw}
          onAcceptDraw={onAcceptDraw}
          onDeclineDraw={onDeclineDraw}
          onResign={onResign}
        />
      )}

      {finished && (
        <div className="space-y-2">
          {onNewGame && (
            <button
              type="button"
              onClick={onNewGame}
              disabled={newGameBusy}
              className="w-full rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
            >
              {creatingNew ? "Création…" : "Nouvelle partie"}
            </button>
          )}
          <RematchControls
            game={game}
            mySeatIndex={mySeatIndex}
            busy={rematching}
            onOffer={onOfferRematch}
            onDecline={onDeclineRematch}
            onGoToRematch={onGoToRematch}
          />
          <button
            type="button"
            onClick={onLeave}
            className="w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100 transition hover:border-zinc-500"
          >
            Quitter
          </button>
        </div>
      )}

      {totalMoves > 0 && (
        <div className="rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2">
          <ReplayNav
            totalMoves={totalMoves}
            step={step}
            inReplay={inReplay}
            onStep={onStep}
            openReplay={openReplay}
            exitReplay={exitReplay}
          />
        </div>
      )}

      <ChatPanel gameId={gameId} playerToken={playerToken} />

      {playerToken !== null && (
        <button
          onClick={onLeave}
          className="text-xs text-zinc-500 hover:text-zinc-300"
        >
          Quitter la partie (efface mon token local)
        </button>
      )}

      {error && (
        <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
          {error}
        </p>
      )}
    </aside>
  );
}
