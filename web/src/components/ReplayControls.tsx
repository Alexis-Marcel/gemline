import { useEffect } from "react";

interface ReplayControlsProps {
  step: number;
  total: number;
  onChange: (step: number) => void;
  onExit: () => void;
}

export function ReplayControls({ step, total, onChange, onExit }: ReplayControlsProps) {
  // Keyboard shortcuts: ←/→ to step, Home/End to jump.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
      switch (e.key) {
        case "ArrowLeft":
          onChange(Math.max(0, step - 1));
          break;
        case "ArrowRight":
          onChange(Math.min(total, step + 1));
          break;
        case "Home":
          onChange(0);
          break;
        case "End":
          onChange(total);
          break;
        case "Escape":
          onExit();
          break;
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step, total, onChange, onExit]);

  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-3">
      <div className="mb-2 flex items-baseline justify-between">
        <h2 className="text-sm font-medium text-zinc-200">Replay</h2>
        <button
          onClick={onExit}
          className="text-xs text-zinc-400 hover:text-zinc-200"
        >
          Quitter (Échap)
        </button>
      </div>
      <div className="flex items-center gap-1">
        <CtrlButton onClick={() => onChange(0)} disabled={step === 0} title="Début (Home)">
          ⏮
        </CtrlButton>
        <CtrlButton
          onClick={() => onChange(Math.max(0, step - 1))}
          disabled={step === 0}
          title="Précédent (←)"
        >
          ◀
        </CtrlButton>
        <CtrlButton
          onClick={() => onChange(Math.min(total, step + 1))}
          disabled={step === total}
          title="Suivant (→)"
        >
          ▶
        </CtrlButton>
        <CtrlButton onClick={() => onChange(total)} disabled={step === total} title="Fin (End)">
          ⏭
        </CtrlButton>
      </div>
      <div className="mt-2 text-xs text-zinc-400">
        Coup <span className="font-mono text-zinc-100">{step}</span> / {total}
      </div>
      <input
        type="range"
        min={0}
        max={total}
        value={step}
        onChange={(e) => onChange(Number(e.target.value))}
        className="mt-2 w-full accent-yellow-500"
      />
    </div>
  );
}

function CtrlButton({
  children,
  onClick,
  disabled,
  title,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
  title?: string;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      title={title}
      className="flex h-8 w-8 items-center justify-center rounded border border-zinc-700 bg-zinc-950 text-zinc-200 transition hover:border-zinc-500 disabled:cursor-not-allowed disabled:opacity-30"
    >
      {children}
    </button>
  );
}
