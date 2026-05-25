import { useEffect, useState } from "react";
import type { Thresholds } from "../api/types";
import { Objectives } from "./Objectives";

interface RulesOverlayProps {
  thresholds: Thresholds;
  open: boolean;
  onClose: () => void;
}

/**
 * RulesOverlay is the controlled modal that paints the Objectives panel
 * over the page. Used both by the standalone ObjectivesPopover button
 * (own its own state) and by the GameBottomBar kebab menu (state lives
 * on the parent GamePage).
 *
 * Dismissal: backdrop click, the X button, or Escape.
 */
export function RulesOverlay({ thresholds, open, onClose }: RulesOverlayProps) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="relative w-full max-w-sm"
        onClick={(e) => e.stopPropagation()}
      >
        <button
          type="button"
          onClick={onClose}
          className="absolute right-2 top-2 z-10 text-zinc-500 hover:text-zinc-200"
          aria-label="Fermer"
        >
          ✕
        </button>
        <Objectives thresholds={thresholds} />
      </div>
    </div>
  );
}

/**
 * ObjectivesPopover is a "?" button that opens RulesOverlay on tap.
 * Kept as the standalone affordance for callers that want a one-shot
 * trigger without managing state themselves.
 */
export function ObjectivesPopover({ thresholds }: { thresholds: Thresholds }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label="Règles de la partie"
        className="inline-flex h-7 w-7 items-center justify-center rounded-full border border-zinc-700 bg-zinc-900 text-xs font-medium text-zinc-300 transition hover:border-zinc-500 hover:text-zinc-100"
      >
        ?
      </button>
      <RulesOverlay
        thresholds={thresholds}
        open={open}
        onClose={() => setOpen(false)}
      />
    </>
  );
}
