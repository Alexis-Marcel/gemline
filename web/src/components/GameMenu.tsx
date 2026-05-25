import { useEffect, useRef, useState } from "react";

interface GameMenuItem {
  label: string;
  onClick: () => void;
  /** Optional visual tag — "danger" tints the row red for destructive
   *  actions (e.g. Quitter). Default is a neutral menu row. */
  variant?: "danger";
}

interface GameMenuProps {
  items: GameMenuItem[];
}

/**
 * GameMenu is the "⋯" kebab dropdown in the GamePage header on mobile.
 * Replaces the inline "Revoir la partie" + "Quitter la partie" links
 * that used to live below the board — once the page locks to a single
 * viewport (overflow-hidden h-dvh), those links had nowhere to live in
 * the visible flow.
 *
 * Tap the trigger to open; tap outside / Escape / pick an item to close.
 * No focus trap or arrow-key navigation yet — items are usually 2-3
 * deep, and the user is likely on a touch device anyway.
 */
export function GameMenu({ items }: GameMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    const onDocClick = (e: MouseEvent) => {
      if (!containerRef.current) return;
      if (!containerRef.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mousedown", onDocClick);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onDocClick);
    };
  }, [open]);

  if (items.length === 0) return null;

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        aria-label="Plus d'actions"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="inline-flex h-7 w-7 items-center justify-center rounded-full border border-zinc-700 bg-zinc-900 text-base font-medium leading-none text-zinc-300 transition hover:border-zinc-500 hover:text-zinc-100"
      >
        ⋯
      </button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 top-full z-40 mt-2 min-w-[12rem] overflow-hidden rounded-md border border-zinc-800 bg-zinc-950 shadow-xl"
        >
          {items.map((item, i) => {
            const danger = item.variant === "danger";
            return (
              <button
                key={i}
                type="button"
                role="menuitem"
                onClick={() => {
                  setOpen(false);
                  item.onClick();
                }}
                className={
                  "block w-full px-3 py-2 text-left text-sm transition " +
                  (danger
                    ? "text-red-300 hover:bg-red-950/40"
                    : "text-zinc-100 hover:bg-zinc-800")
                }
              >
                {item.label}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
