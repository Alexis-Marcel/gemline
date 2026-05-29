import { Fragment, Suspense, lazy, useMemo } from "react";
import type { Color } from "../api/types";
import { axialToScreen, boardPositions, cellIndex, inBoard } from "../lib/hex";
import { gemColor } from "../lib/colors";

// Lazy so the ~13 KB-gzipped pinch-zoom chunk only ships to coarse-pointer clients.
const BoardPinchWrapper = lazy(() => import("./BoardPinchWrapper"));

const UNIT = 36;
const STONE_RADIUS = UNIT * 0.42;
const DOT_RADIUS = 2.5;

interface BoardProps {
  side: number;
  cells: Color[];
  // Board is stateless w.r.t. the tap flow; the parent owns commit-vs-preview.
  onCellTap?: (q: number, r: number) => void;
  // Tap on dead canvas; parent uses it to cancel a pending preview.
  onBoardTap?: () => void;
  pendingCell?: { q: number; r: number } | null;
  highlight?: { q: number; r: number } | null;
  disabled?: boolean;
  ghosts?: Array<{ q: number; r: number; color: Color; key: string }>;
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

  // Coarse-pointer detection: only decides whether to mount the pinch wrapper.
  const isCoarsePointer = useMemo(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia("(pointer: coarse)").matches,
    [],
  );

  // Bounding box padded so edge stones don't get clipped.
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
      // touch-manipulation kills the iOS 300ms tap-delay and double-tap-zoom;
      // the tap-highlight override removes Safari's grey flash on tapped SVG.
      className="w-full h-full select-none touch-manipulation [-webkit-tap-highlight-color:transparent]"
      role="img"
      aria-label="Plateau hexagonal Gemline"
      // Taps not on a cell bubble here (cells stopPropagation) to cancel preview.
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
                // 0.48 = largest hitbox keeping a sliver of dead space between
                // adjacent cells (0.96*UNIT < UNIT), maximizing finger target.
                r={UNIT * 0.48}
                fill="transparent"
                className="cursor-pointer hover:fill-white/10"
                onClick={(e) => {
                  // Don't bubble: onBoardTap would clear the preview we're arming.
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
          <circle
            cx={pendingScreen.x}
            cy={pendingScreen.y}
            r={STONE_RADIUS}
            fill={pendingColor}
            opacity={0.55}
            stroke="#0a0a0a"
            strokeWidth={1}
          />
          {/* pointerEvents off so the cell hitbox underneath stays the target. */}
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

  // Desktop keeps the bare SVG; only touch gets pinch/pan.
  if (!isCoarsePointer) {
    return svgEl;
  }
  // Fallback is the bare SVG so play stays possible while the pinch chunk loads.
  return (
    <Suspense fallback={svgEl}>
      <BoardPinchWrapper>{svgEl}</BoardPinchWrapper>
    </Suspense>
  );
}
