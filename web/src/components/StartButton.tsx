import type { Game } from "../api/types";

interface StartButtonProps {
  game: Game;
  onStart: () => void;
}

// Disabled until 2+ seats are occupied; the server starts with N occupied
// players and drops the empty seats regardless of the room's max.
export function StartButton({ game, onStart }: StartButtonProps) {
  const occupied = game.seats.filter((s) => s.occupied).length;
  const ready = occupied >= 2;
  return (
    <button
      type="button"
      onClick={onStart}
      disabled={!ready}
      className={
        "w-full rounded-xl border px-4 py-3 text-left transition disabled:cursor-not-allowed " +
        (ready
          ? "border-amber-400 bg-amber-400/10 text-amber-100 hover:bg-amber-400/20"
          : "border-zinc-800 bg-zinc-900/30 text-zinc-500")
      }
    >
      <div className="text-sm font-medium">Lancer la partie</div>
      <div className="mt-0.5 text-[11px]">
        {ready
          ? `${occupied} joueur${occupied > 1 ? "s" : ""} — les sièges vides resteront vides.`
          : "Au moins 2 sièges occupés (invite un joueur ou ajoute un bot)."}
      </div>
    </button>
  );
}
