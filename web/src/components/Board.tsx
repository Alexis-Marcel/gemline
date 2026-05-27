import { Fragment, Suspense, lazy, useMemo } from "react";
import type { Color } from "../api/types";
import { axialToScreen, boardPositions, cellIndex, inBoard } from "../lib/hex";
import { gemColor } from "../lib/colors";

// Lazy-load the pinch-zoom wrapper so its ~13 KB-gzipped JS chunk
// only ships to clients that actually need it (coarse pointers).
// Desktop never downloads it.
const BoardPinchWrapper = lazy(() => import("./BoardPinchWrapper"));

const UNIT = 36;
const STONE_RADIUS = UNIT * 0.42;
const DOT_RADIUS = 2.5;

interface BoardProps {
  side: number;
  cells: Color[];
  /** Tap callback for free intersections. The parent decides whether to
   *  commit immediately (mouse / fine pointer) or arm a preview ghost
   *  via `pendingCell` and re-tap to commit (touch / coarse pointer).
   *  Board itself is stateless w.r.t. the tap flow now. */
  onCellTap?: (q: number, r: number) => void;
  /** Tap callback for the surrounding board canvas (gridlines, existing
   *  stones, dead space). Used by the parent to clear a pending preview
   *  — "tap elsewhere = cancel" without a dedicated gesture. */
  onBoardTap?: () => void;
  /** Currently armed preview cell, or null. When set, Board paints a
   *  semi-opaque ghost in `playerColor` with a dashed amber ring.
   *  Stateless on Board's side — the parent owns when to set / clear. */
  pendingCell?: { q: number; r: number } | null;
  highlight?: { q: number; r: number } | null;
  disabled?: boolean;
  /** Stones to animate as "just captured". Rendered on top of the live board
   *  with a fade-out so the user sees what was removed. */
  ghosts?: Array<{ q: number; r: number; color: Color; key: string }>;
  /** Local player's stone colour. Used to tint the preview ghost when
   *  `pendingCell` is set; ignored otherwise. */
  playerColor?: Color;
}

export function Board({
  side,
  cells,
  onCellTap,
  onBoardTap,
  pendingCell,
  highlight,
  disabled,
  ghosts,
  playerColor,
}: BoardProps) {
  const positions = useMemo(() => boardPositions(side), [side]);

  // Coarse-pointer (touch / stylus) detection — used only to decide whether
  // to mount the pinch-zoom wrapper around the SVG. The tap-to-confirm flow
  // itself lives in the parent (GamePage) now, so the decision to arm a
  // preview vs. commit immediately is taken there using the same check.
  const isCoarsePointer = useMemo(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia("(pointer: coarse)").matches,
    [],
  );

  // Compute the bounding box from the actual rendered intersection positions
  // and pad it so stones don't get clipped.
  const screen = useMemo(
    () => positions.map((p) => axialToScreen(p.q, p.r, UNIT)),
    [positions],
  );
  const xs = screen.map((p) => p.x);
  const ys = screen.map((p) => p.y);
  const minX = Math.min(...xs) - STONE_RADIUS - 8;
  const maxX = Math.max(...xs) + STONE_RADIUS + 8;
  const minY = Math.min(...ys) - STONE_RADIUS - 8;
  const maxY = Math.max(...ys) + STONE_RADIUS + 8;
  const viewBox = `${minX} ${minY} ${maxX - minX} ${maxY - minY}`;

  // Pre-compute the line segments along the three axes for the triangular
  // grid background. Each segment connects two adjacent intersections.
  const segments = useMemo(() => {
    const dirs: Array<[number, number]> = [
      [1, 0],
      [0, 1],
      [1, -1],
    ];
    const out: Array<{ x1: number; y1: number; x2: number; y2: number }> = [];
    for (const { q, r } of positions) {
      for (const [dq, dr] of dirs) {
        const nq = q + dq;
        const nr = r + dr;
        if (!inBoard(nq, nr, side)) continue;
        const a = axialToScreen(q, r, UNIT);
        const b = axialToScreen(nq, nr, UNIT);
        out.push({ x1: a.x, y1: a.y, x2: b.x, y2: b.y });
      }
    }
    return out;
  }, [positions, side]);

  const pendingColor =
    pendingCell && playerColor !== undefined ? gemColor(playerColor) : null;
  const pendingScreen = pendingCell
    ? axialToScreen(pendingCell.q, pendingCell.r, UNIT)
    : null;

  const svgEl = (
    <svg
      viewBox={viewBox}
      // touch-action: manipulation kills the 300ms tap-delay heuristic and
      // the browser's double-tap-to-zoom on the board, both of which were
      // making rapid placements feel laggy on iOS. The -webkit-tap-highlight
      // override removes the grey flash that Safari paints under every tap
      // on a clickable SVG element.
      className="w-full h-full select-none touch-manipulation [-webkit-tap-highlight-color:transparent]"
      role="img"
      aria-label="Plateau hexagonal Gemline"
      // A click on the SVG canvas that didn't land on a clickable cell
      // bubbles here (cell circles stopPropagation). The parent typically
      // uses this to cancel a pending preview — "tap elsewhere = nevermind".
      onClick={onBoardTap}
    >
      <g stroke="#3f3f46" strokeWidth={0.6} strokeLinecap="round">
        {segments.map((s, i) => (
          <line key={i} x1={s.x1} y1={s.y1} x2={s.x2} y2={s.y2} />
        ))}
      </g>

      {positions.map(({ q, r }) => {
        const { x, y } = axialToScreen(q, r, UNIT);
        const c = cells[cellIndex(q, r, side)];
        const color = gemColor(c);
        const isHighlight = highlight?.q === q && highlight?.r === r;
        const clickable = !disabled && c === 0 && onCellTap;

        return (
          <Fragment key={`${q},${r}`}>
            {color === null && (
              <circle cx={x} cy={y} r={DOT_RADIUS} fill="#52525b" />
            )}
            {color !== null && (
              <circle
                cx={x}
                cy={y}
                r={STONE_RADIUS}
                fill={color}
                stroke="#0a0a0a"
                strokeWidth={1}
              />
            )}
            {isHighlight && (
              <circle
                cx={x}
                cy={y}
                r={STONE_RADIUS + 3}
                fill="none"
                stroke="#fbbf24"
                strokeWidth={2}
              />
            )}
            {clickable && (
              <circle
                cx={x}
                cy={y}
                // r = UNIT * 0.48 is the largest hitbox that still leaves a
                // sliver of dead space between adjacent intersections (centers
                // are exactly UNIT apart, so r1+r2 = 0.96*UNIT < UNIT). Bumped
                // up from 0.45 to give the finger a bit more room on mobile,
                // where the scaled radius was ~7 px (well under the 22 px
                // Apple HIG minimum).
                r={UNIT * 0.48}
                fill="transparent"
                className="cursor-pointer hover:fill-white/10"
                onClick={(e) => {
                  // Don't bubble — the SVG-level onClick (onBoardTap) would
                  // otherwise clear the preview we're about to arm.
                  e.stopPropagation();
                  onCellTap?.(q, r);
                }}
              />
            )}
          </Fragment>
        );
      })}

      {pendingCell && pendingColor && pendingScreen && (
        <>
          {/* Preview stone — semi-opaque to read as "not committed yet". */}
          <circle
            cx={pendingScreen.x}
            cy={pendingScreen.y}
            r={STONE_RADIUS}
            fill={pendingColor}
            opacity={0.55}
            stroke="#0a0a0a"
            strokeWidth={1}
          />
          {/* Dashed amber ring to make the "tap again to confirm"
             affordance unambiguous. Pointer-events disabled so the cell
             hitbox underneath stays the click target. */}
          <circle
            cx={pendingScreen.x}
            cy={pendingScreen.y}
            r={STONE_RADIUS + 3}
            fill="none"
            stroke="#fbbf24"
            strokeWidth={2}
            strokeDasharray="3 3"
            pointerEvents="none"
          />
        </>
      )}

      {ghosts &&
        ghosts.map((g) => {
          const { x, y } = axialToScreen(g.q, g.r, UNIT);
          const color = gemColor(g.color) ?? "#a1a1aa";
          return (
            <Fragment key={g.key}>
              <circle
                cx={x}
                cy={y}
                r={STONE_RADIUS}
                fill={color}
                stroke="#0a0a0a"
                strokeWidth={1}
                className="capture-ghost"
              />
              <circle
                cx={x}
                cy={y}
                r={STONE_RADIUS}
                stroke={color}
                strokeWidth={2}
                className="capture-ring"
              />
            </Fragment>
          );
        })}
    </svg>
  );

  // Pinch-to-zoom + pan on touch devices only. Mouse / desktop keeps the
  // bare SVG — wheel zoom on a chess.com-style board is more annoying than
  // useful, and there's nothing to pan since the whole board fits the
  // viewport on a real screen.
  //
  // doubleClick is disabled because the tap-to-confirm flow already uses
  // "two taps on the same cell" semantically; the library's default
  // double-click-to-zoom would otherwise eat the confirmation.
  if (!isCoarsePointer) {
    return svgEl;
  }
  // Suspense fallback is the bare SVG — while the pinch chunk loads
  // (one-shot, the first game on a touch device), the user can still
  // tap to play. Once it arrives, the wrapper hot-swaps in and gestures
  // become available with no remount needed.
  return (
    <Suspense fallback={svgEl}>
      <BoardPinchWrapper>{svgEl}</BoardPinchWrapper>
    </Suspense>
  );
}
