import { useEffect, useRef, useState } from "react";
import { ReplayNav } from "./ReplayNav";

export interface BottomBarMenuItem {
  label: string;
  onClick: () => void;
  variant?: "danger" | "primary";
  disabled?: boolean;
}

interface GameBottomBarProps {
  /** Number of moves played — when 0, the replay-nav block hides. */
  totalMoves: number;
  /** Currently displayed step (0..totalMoves). Equal to totalMoves when
   *  the user is on the live board; less when stepping through history. */
  step: number;
  /** True while the user is in replay mode (i.e. the board renders a
   *  past step rather than live state). */
  inReplay: boolean;
  /** Called with the new step on ◀ / ▶ taps. The host owns the live →
   *  replay transition: tapping ◀ on a live game opens replay at the
   *  last step (totalMoves), then subsequent taps decrement. */
  onStep: (step: number) => void;
  /** Asynchronous "enter replay" hook for the live → replay transition.
   *  Tapping ◀ when not yet in replay calls this; subsequent ◀ taps go
   *  through onStep. The bar waits for openReplay to finish before
   *  considering the move applied. */
  openReplay: () => void;
  /** Called when the user taps the live/exit button to leave replay
   *  mode. No-op when not in replay. */
  exitReplay: () => void;
  /** Called when the chat icon is tapped. The chat surface itself
   *  (ChatDrawer on mobile, inline panel on desktop) is owned by the
   *  parent. */
  onOpenChat: () => void;
  /** Menu items rendered behind the ⋯ kebab. Empty array → kebab still
   *  renders but disabled (gives the bar a stable layout). */
  menuItems: BottomBarMenuItem[];
}

/**
 * GameBottomBar is the fixed action toolbar at the bottom of the
 * GamePage. Three regions:
 *
 *    [💬]            [◀  N / M  ▶]                       [⋯]
 *
 *   left  : chat trigger (always)
 *   centre: replay navigation (only when there are moves to step through)
 *   right : kebab menu — the catch-all for "everything else" — propose
 *           draw, resign, start the game, leave, rules. The menu items
 *           are passed in so each game state can dictate what shows up
 *           without the bar having to know about game logic.
 *
 * Pinned to the bottom of the viewport via `fixed`, with the iOS safe-
 * area inset baked into the bottom padding so the home-indicator strip
 * doesn't crowd the controls.
 */
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

  // Close on Escape / outside click. Mirrors the GameMenu pattern.
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
      {/* Left: chat */}
      <button
        type="button"
        onClick={onOpenChat}
        aria-label="Ouvrir le chat"
        className="inline-flex h-10 w-10 items-center justify-center rounded-full border border-zinc-700 bg-zinc-900 text-base transition hover:border-zinc-500"
      >
        💬
      </button>

      {/* Centre: replay nav. Hidden when there are no moves yet; the
         spacer keeps the kebab anchored to the right via
         justify-between. */}
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

      {/* Right: kebab */}
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
