import type { Color } from "../api/types";

// Six well-differentiated hues for the player gems, plus auxiliary colors
// for the empty board and capture highlights.
export const GEM_COLORS: Record<Exclude<Color, -1 | 0>, string> = {
  1: "#ef4444", // red-500
  2: "#3b82f6", // blue-500
  3: "#22c55e", // green-500
  4: "#eab308", // yellow-500
  5: "#a855f7", // purple-500
  6: "#f97316", // orange-500
};

export const GEM_NAMES: Record<Exclude<Color, -1 | 0>, string> = {
  1: "Rouge",
  2: "Bleu",
  3: "Vert",
  4: "Jaune",
  5: "Violet",
  6: "Orange",
};

export function gemColor(c: Color): string | null {
  if (c <= 0) return null;
  return GEM_COLORS[c as 1 | 2 | 3 | 4 | 5 | 6];
}

export function gemName(c: Color): string {
  if (c <= 0) return "—";
  return GEM_NAMES[c as 1 | 2 | 3 | 4 | 5 | 6];
}
