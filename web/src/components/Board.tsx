import { Fragment, useEffect, useMemo, useState } from "react";
import { TransformComponent, TransformWrapper } from "react-zoom-pan-pinch";
import type { Color } from "../api/types";
import { axialToScreen, boardPositions, cellIndex, inBoard } from "../lib/hex";
import { gemColor } from "../lib/colors";

const UNIT = 36;
const STONE_RADIUS = UNIT * 0.42;
const DOT_RADIUS = 2.5;

interface BoardProps {
  side: number;
  cells: Color[];
  onPlay?: (q: number, r: number) => void;
  highlight?: { q: number; r: number } | null;
  disabled?: boolean;
  /** Stones to animate as "just captured". Rendered on top of the live board
   *  with a fade-out so the user sees what was removed. */
  ghosts?: Array<{ q: number; r: number; color: Color; key: string }>;
  /** The local player's stone color. When provided AND the user is on a
   *  coarse-pointer device, a first tap on a free intersection paints a
   *  preview ghost of this color and a second tap on the same cell commits
   *  the move — tap-to-confirm guards against mis-taps on tight phone
   *  hitboxes. Omit (or leave undefined) to keep the immediate-place
   *  behaviour everywhere; mouse / desktop users always get immediate
   *  placement regardless. */
  playerColor?: Color;
}

export function Board({
  side,
  cells,
  onPlay,
  highlight,
  disabled,
  ghosts,
  playerColor,
}: BoardProps) {
  const positions = useMemo(() => boardPositions(side), [side]);

  // Detect coarse pointers (touch / stylus) once at mount. matchMedia is
  // a stable enough signal — devices don't switch input modes mid-game
  // in practice — and it lets us skip the tap-to-confirm dance entirely
  // for mouse users where mis-clicks are vanishingly rare.
  const isCoarsePointer = useMemo(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia("(pointer: coarse)").matches,
    [],
  );

  // The pending placement preview. Only used on coarse-pointer devices —
  // see `isCoarsePointer`. We clear it whenever the board state changes
  // out from under us (an opponent moved, the game ended, the player's
  // turn flipped) so a stale ghost can't survive a state transition and
  // accidentally commit on the next tap.
  const [pendingCell, setPendingCell] = useState<{ q: number; r: number } | null>(
    null,
  );
  useEffect(() => {
    setPendingCell(null);
  }, [cells, disabled]);

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

  function handleCellClick(q: number, r: number) {
    if (!onPlay) return;
    if (!isCoarsePointer || !playerColor) {
      onPlay(q, r);
      return;
    }
    // Coarse pointer with a known player colour: first tap arms the
    // preview, second tap on the same cell commits. A different cell
    // moves the preview; a tap outside any clickable cell bubbles to
    // the SVG and cancels (handled below).
    if (pendingCell && pendingCell.q === q && pendingCell.r === r) {
      setPendingCell(null);
      onPlay(q, r);
    } else {
      setPendingCell({ q, r });
    }
  }

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
      // (cell-circle clicks stopPropagation) cancels any pending preview.
      // Gives users an escape hatch without having to invent another
      // gesture — tap "elsewhere" reads as "nevermind".
      onClick={() => setPendingCell(null)}
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
        const clickable = !disabled && c === 0 && onPlay;

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
                  // Don't bubble — the SVG-level onClick would otherwise
                  // clear the preview we're about to arm.
                  e.stopPropagation();
                  handleCellClick(q, r);
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
  return (
    <TransformWrapper
      initialScale={1}
      minScale={1}
      maxScale={3}
      doubleClick={{ disabled: true }}
      wheel={{ disabled: true }}
      panning={{ velocityDisabled: true }}
    >
      <TransformComponent wrapperClass="!w-full !h-full" contentClass="!w-full !h-full">
        {svgEl}
      </TransformComponent>
    </TransformWrapper>
  );
}
