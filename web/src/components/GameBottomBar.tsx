import { useEffect, useRef, useState } from "react";
import { ReplayNav } from "./ReplayNav";

export interface BottomBarMenuItem {
  label: string;
  onClick: () => void;
  variant?: "danger" | "primary";
  disabled?: boolean;
}

interface GameBottomBarProps {
  totalMoves: number;
  step: number;
  inReplay: boolean;
  onStep: (step: number) => void;
  // Async live -> replay transition; subsequent ◀ taps go through onStep.
  openReplay: () => void;
  exitReplay: () => void;
  onOpenChat: () => void;
  // Empty array still renders the kebab (disabled) for stable layout.
  menuItems: BottomBarMenuItem[];
}

// iOS safe-area inset is baked into the bottom padding so the home-indicator
// strip doesn't crowd the controls.
export function GameBottomBar({
  totalMoves,
  step,
  inReplay,
  onStep,
  openReplay,
  exitReplay,
  onOpenChat,
  menuItems,
}: GameBottomBarProps) {
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  // Close on Escape / outside click.
  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMenuOpen(false);
    };
    const onDocClick = (e: MouseEvent) => {
      if (!menuRef.current) return;
      if (!menuRef.current.contains(e.target as Node)) setMenuOpen(false);
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mousedown", onDocClick);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onDocClick);
    };
  }, [menuOpen]);

  const hasReplay = totalMoves > 0;

  return (
    <nav className="fixed inset-x-0 bottom-0 z-30 flex items-center justify-between gap-2 border-t border-zinc-800 bg-zinc-950/95 px-3 py-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] backdrop-blur">
      <button
        type="button"
        onClick={onOpenChat}
        aria-label="Ouvrir le chat"
        className="inline-flex h-10 w-10 items-center justify-center rounded-full border border-zinc-700 bg-zinc-900 text-base transition hover:border-zinc-500"
      >
        💬
      </button>

      {/* Spacer keeps the kebab right-anchored when replay nav is hidden. */}
      {hasReplay ? (
        <ReplayNav
          totalMoves={totalMoves}
          step={step}
          inReplay={inReplay}
          onStep={onStep}
          openReplay={openReplay}
          exitReplay={exitReplay}
        />
      ) : (
        <div />
      )}

      <div ref={menuRef} className="relative">
        <button
          type="button"
          aria-label="Plus d'actions"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          disabled={menuItems.length === 0}
          onClick={() => setMenuOpen((v) => !v)}
          className="inline-flex h-10 w-10 items-center justify-center rounded-full border border-zinc-700 bg-zinc-900 text-base font-medium leading-none text-zinc-300 transition hover:border-zinc-500 hover:text-zinc-100 disabled:opacity-40"
        >
          ⋯
        </button>
        {menuOpen && (
          <div
            role="menu"
            className="absolute bottom-full right-0 z-40 mb-2 min-w-[14rem] overflow-hidden rounded-md border border-zinc-800 bg-zinc-950 shadow-xl"
          >
            {menuItems.map((item, i) => {
              const cls =
                item.variant === "danger"
                  ? "text-red-300 hover:bg-red-950/40 disabled:opacity-40"
                  : item.variant === "primary"
                    ? "text-amber-200 hover:bg-amber-950/30 disabled:opacity-40"
                    : "text-zinc-100 hover:bg-zinc-800 disabled:opacity-40";
              return (
                <button
                  key={i}
                  type="button"
                  role="menuitem"
                  disabled={item.disabled}
                  onClick={() => {
                    setMenuOpen(false);
                    item.onClick();
                  }}
                  className={
                    "block w-full px-3 py-2 text-left text-sm transition disabled:cursor-not-allowed " +
                    cls
                  }
                >
                  {item.label}
                </button>
              );
            })}
          </div>
        )}
      </div>
    </nav>
  );
}
