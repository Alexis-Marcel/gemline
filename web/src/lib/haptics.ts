// Thin wrapper around navigator.vibrate so callers don't have to deal
// with the "does this browser support it?" / "is the user on iOS Safari
// where this is a no-op?" checks every time. iOS Safari does NOT
// implement vibrate — the calls return false silently. Chrome on
// Android and Firefox on Android do; that's our happy path.

type Pattern = number | number[];

function vibrate(pattern: Pattern): void {
  if (typeof navigator === "undefined" || !navigator.vibrate) return;
  try {
    navigator.vibrate(pattern);
  } catch {
    /* swallow — vibrate can throw in some sandboxed contexts */
  }
}

/** A short, sharp pulse — for "something just happened on the board"
 *  events (your stone went down, opponent captured your pair, etc.). */
export function hapticTap(): void {
  vibrate(10);
}

/** A heavier double-pulse for capture moves — the player just took
 *  pieces off the board, more visceral than a regular move. */
export function hapticCapture(): void {
  vibrate([15, 30, 15]);
}

/** A longer pattern for the end of the game — gives the moment some
 *  weight without being annoying. Used regardless of who won. */
export function hapticGameEnd(): void {
  vibrate([30, 50, 30, 50, 60]);
}
