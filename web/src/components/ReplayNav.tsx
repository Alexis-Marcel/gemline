interface ReplayNavProps {
  totalMoves: number;
  step: number;
  inReplay: boolean;
  onStep: (step: number) => void;
  openReplay: () => void;
  exitReplay: () => void;
}

// Tapping ◀ on a live game enters replay at the last step.
export function ReplayNav({
  totalMoves,
  step,
  inReplay,
  onStep,
  openReplay,
  exitReplay,
}: ReplayNavProps) {
  function back() {
    if (!inReplay) {
      openReplay();
      return;
    }
    onStep(Math.max(0, step - 1));
  }
  function forward() {
    if (!inReplay) return;
    onStep(Math.min(totalMoves, step + 1));
  }

  return (
    <div className="flex items-center gap-2 text-xs text-zinc-400">
      <button
        type="button"
        onClick={back}
        disabled={inReplay && step === 0}
        aria-label="Coup précédent"
        className="inline-flex h-8 w-8 items-center justify-center rounded border border-zinc-700 bg-zinc-950 text-zinc-200 transition hover:border-zinc-500 disabled:cursor-not-allowed disabled:opacity-30"
      >
        ◀
      </button>
      <span className="font-mono tabular-nums">
        {inReplay ? step : totalMoves}
        <span className="text-zinc-600">/</span>
        {totalMoves}
      </span>
      <button
        type="button"
        onClick={forward}
        disabled={!inReplay || step === totalMoves}
        aria-label="Coup suivant"
        className="inline-flex h-8 w-8 items-center justify-center rounded border border-zinc-700 bg-zinc-950 text-zinc-200 transition hover:border-zinc-500 disabled:cursor-not-allowed disabled:opacity-30"
      >
        ▶
      </button>
      {inReplay && (
        <button
          type="button"
          onClick={exitReplay}
          className="text-[11px] text-amber-300 hover:text-amber-200"
          aria-label="Quitter le replay"
        >
          live
        </button>
      )}
    </div>
  );
}
