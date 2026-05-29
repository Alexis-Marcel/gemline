import { useEffect } from "react";
import { ChatPanel } from "./ChatPanel";

interface ChatDrawerProps {
  open: boolean;
  onClose: () => void;
  gameId: string;
  playerToken: string | null;
}

// Mobile chat bottom sheet. Always mounted so the slide transition can
// animate; pointer-events-none keeps the hidden backdrop from eating taps.
export function ChatDrawer({ open, onClose, gameId, playerToken }: ChatDrawerProps) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  return (
    <div
      className={`fixed inset-0 z-40 bg-black/60 transition-opacity duration-200 ${
        open ? "opacity-100" : "pointer-events-none opacity-0"
      }`}
      onClick={onClose}
      aria-hidden={!open}
    >
      <div
        className={`absolute inset-x-0 bottom-0 flex h-[80dvh] flex-col rounded-t-2xl border-t border-zinc-800 bg-zinc-950 shadow-2xl transition-transform duration-200 ease-out ${
          open ? "translate-y-0" : "translate-y-full"
        }`}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
          <div className="absolute inset-x-0 top-1.5 flex justify-center" aria-hidden>
            <div className="h-1 w-10 rounded-full bg-zinc-700" />
          </div>
          <h2 className="text-sm font-medium text-zinc-200">Chat</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Fermer le chat"
            className="text-zinc-500 hover:text-zinc-200"
          >
            ✕
          </button>
        </header>
        <div className="min-h-0 flex-1">
          <ChatPanel gameId={gameId} playerToken={playerToken} embedded />
        </div>
      </div>
    </div>
  );
}
