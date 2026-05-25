import { useEffect } from "react";
import type { Thresholds } from "../api/types";
import { Objectives } from "./Objectives";

interface RulesOverlayProps {
  thresholds: Thresholds;
  open: boolean;
  onClose: () => void;
}

/**
 * RulesOverlay paints the Objectives panel as a centered modal over
 * the page. Controlled — the parent owns the open/closed state so the
 * trigger can live anywhere. Today it's the GameBottomBar kebab
 * "Règles de la partie" item on mobile; desktop has the same content
 * always inline in the right rail and never opens this overlay.
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
