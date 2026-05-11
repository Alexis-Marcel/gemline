// Hex grid geometry helpers. The board uses axial coordinates (q, r) centered
// at (0, 0); the screen origin is also (0, 0) — translate via the viewBox.

export interface Axial {
  q: number;
  r: number;
}

/**
 * axialToScreen maps an axial coordinate to a Cartesian (SVG) coordinate.
 * `unit` is the spacing between adjacent intersections.
 */
export function axialToScreen(q: number, r: number, unit: number): { x: number; y: number } {
  return {
    x: unit * (q + r / 2),
    y: unit * r * (Math.sqrt(3) / 2),
  };
}

/**
 * cellIndex converts an axial coordinate to a flat-array index that matches
 * the server's `cells` layout: row-major over the (2·side−1) × (2·side−1)
 * bounding box, with (0,0) at the center.
 */
export function cellIndex(q: number, r: number, side: number): number {
  const span = 2 * side - 1;
  return (r + side - 1) * span + (q + side - 1);
}

/**
 * inBoard reports whether (q, r) is a valid intersection for a hex of `side`.
 */
export function inBoard(q: number, r: number, side: number): boolean {
  const radius = side - 1;
  return (
    Math.abs(q) <= radius && Math.abs(r) <= radius && Math.abs(q + r) <= radius
  );
}

/**
 * boardPositions yields every valid axial position on the hex of `side`,
 * once each, in scan order.
 */
export function boardPositions(side: number): Axial[] {
  const r = side - 1;
  const out: Axial[] = [];
  for (let qi = -r; qi <= r; qi++) {
    for (let ri = -r; ri <= r; ri++) {
      if (inBoard(qi, ri, side)) out.push({ q: qi, r: ri });
    }
  }
  return out;
}
