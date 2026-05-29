// Wrapper around navigator.vibrate. iOS Safari doesn't implement it (calls
// return false silently); Chrome/Firefox on Android are the happy path.

type Pattern = number | number[];

function vibrate(pattern: Pattern): void {
  if (typeof navigator === "undefined" || !navigator.vibrate) return;
  try {
    navigator.vibrate(pattern);
  } catch {
    // vibrate can throw in some sandboxed contexts
  }
}

export function hapticTap(): void {
  vibrate(10);
}

export function hapticCapture(): void {
  vibrate([15, 30, 15]);
}

export function hapticGameEnd(): void {
  vibrate([30, 50, 30, 50, 60]);
}
