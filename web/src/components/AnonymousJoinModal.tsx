import { useState } from "react";

interface AnonymousJoinModalProps {
  seatsFree: number;
  initialName: string;
  submitting: boolean;
  onSubmit: (name: string) => void | Promise<void>;
}

// Guests only: the server has no other way to identify an anon seat.
// Blocking by design (no backdrop close / X) since there's nothing to do
// in the game until a name is provided.
export function AnonymousJoinModal({
  seatsFree,
  initialName,
  submitting,
  onSubmit,
}: AnonymousJoinModalProps) {
  const [name, setName] = useState(initialName);
  const trimmed = name.trim();
  const disabled = submitting || trimmed.length === 0;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (disabled) return;
          void onSubmit(trimmed);
        }}
        className="w-full max-w-sm space-y-3 rounded-2xl border border-zinc-800 bg-zinc-950 p-6 shadow-2xl"
      >
        <header>
          <h2 className="text-lg font-semibold text-zinc-100">
            Rejoindre la partie
          </h2>
          <p className="mt-1 text-xs text-zinc-400">
            {seatsFree > 0
              ? `${seatsFree} place${seatsFree > 1 ? "s" : ""} libre${seatsFree > 1 ? "s" : ""}. Choisis un pseudo.`
              : "Plus de place — tu pourras observer la partie."}
          </p>
        </header>
        <input
          autoFocus
          className="block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
          placeholder="Ton nom"
          value={name}
          maxLength={32}
          onChange={(e) => setName(e.target.value)}
          disabled={seatsFree === 0}
        />
        <button
          type="submit"
          disabled={disabled || seatsFree === 0}
          className="w-full rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
        >
          {submitting ? "…" : "Rejoindre"}
        </button>
        <p className="text-[11px] text-zinc-500">
          Ou{" "}
          <a href="/login" className="text-amber-400 hover:underline">
            connecte-toi
          </a>{" "}
          pour jouer sous ton nom de profil.
        </p>
      </form>
    </div>
  );
}
