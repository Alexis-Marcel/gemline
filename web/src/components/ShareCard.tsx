/**
 * ShareCard renders the read-only invite URL for a private waiting lobby.
 * Clicking selects the field so the host can copy + paste in their chat
 * channel of choice. No "copy" button on purpose — the field is short
 * enough that selection-then-copy is one keystroke faster than a button.
 */
export function ShareCard({ id }: { id: string }) {
  const url = `${window.location.origin}/game/${id}`;
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-3 text-xs text-zinc-400">
      <div className="mb-1 font-medium text-zinc-300">Inviter</div>
      <input
        readOnly
        value={url}
        onFocus={(e) => e.currentTarget.select()}
        className="w-full rounded border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-300"
      />
    </div>
  );
}
