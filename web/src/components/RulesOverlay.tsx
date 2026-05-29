import { useEffect } from "react";
import type { Thresholds } from "../api/types";
import { Objectives } from "./Objectives";

interface RulesOverlayProps {
  thresholds: Thresholds;
  open: boolean;
  onClose: () => void;
}

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
