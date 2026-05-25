import { useEffect, useState } from "react";
import type { Thresholds } from "../api/types";
import { Objectives } from "./Objectives";

/**
 * ObjectivesPopover is the mobile-only "?" button in the GamePage header
 * that reveals the Objectives panel as a centered overlay. The desktop
 * layout still renders <Objectives> inline in the right rail (where
 * there's room); on phones the same information lives one tap away
 * without occupying ~150 px of vertical real estate during play.
 *
 * Dismissal: backdrop click, the X button, or Escape. We intentionally
 * don't trap focus or block scroll under the modal — the dialog is
 * small and read-only, so a more elaborate focus dance would be
 * overkill.
 */
export function ObjectivesPopover({ thresholds }: { thresholds: Thresholds }) {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

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
      {open && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4"
          onClick={() => setOpen(false)}
        >
          <div
            className="relative w-full max-w-sm"
            onClick={(e) => e.stopPropagation()}
          >
            <button
              type="button"
              onClick={() => setOpen(false)}
              className="absolute right-2 top-2 z-10 text-zinc-500 hover:text-zinc-200"
              aria-label="Fermer"
            >
              ✕
            </button>
            <Objectives thresholds={thresholds} />
          </div>
        </div>
      )}
    </>
  );
}
