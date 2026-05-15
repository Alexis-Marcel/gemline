// Lightweight notification chime synthesised at call time via the Web
// Audio API — no asset to ship, no autoplay headaches once the user
// has interacted with the page at least once.
//
// Browser policy: AudioContext stays suspended until a user gesture.
// We lazily create the context on first play; if it's suspended at
// that point the browser silently does nothing, which is the desired
// fallback (we don't want to spam console errors). After the user
// clicks anywhere we'll resume automatically on the next play.

let ctx: AudioContext | null = null;

function getCtx(): AudioContext | null {
  if (typeof window === "undefined") return null;
  if (ctx) return ctx;
  const Ctor =
    window.AudioContext ??
    (window as unknown as { webkitAudioContext?: typeof AudioContext })
      .webkitAudioContext;
  if (!Ctor) return null;
  try {
    ctx = new Ctor();
  } catch {
    ctx = null;
  }
  return ctx;
}

/**
 * Play a short two-tone "ding-dong" pattern. Safe to call repeatedly;
 * each call schedules its own pair of oscillators that clean themselves
 * up. Returns immediately — playback is async.
 */
export function playNotificationSound() {
  const ac = getCtx();
  if (!ac) return;
  // Some browsers ship the context suspended; resume() returns a
  // promise we don't await — if the user hasn't interacted yet it
  // rejects and we just skip this play.
  if (ac.state === "suspended") {
    ac.resume().catch(() => undefined);
  }
  const now = ac.currentTime;
  playTone(ac, 880, now, 0.12);
  playTone(ac, 660, now + 0.13, 0.16);
}

function playTone(
  ac: AudioContext,
  freq: number,
  startAt: number,
  duration: number,
) {
  const osc = ac.createOscillator();
  const gain = ac.createGain();
  osc.type = "sine";
  osc.frequency.value = freq;
  // Tiny linear ramp on the gain to avoid clicks at start/stop.
  gain.gain.setValueAtTime(0, startAt);
  gain.gain.linearRampToValueAtTime(0.08, startAt + 0.01);
  gain.gain.setValueAtTime(0.08, startAt + duration - 0.04);
  gain.gain.linearRampToValueAtTime(0, startAt + duration);
  osc.connect(gain);
  gain.connect(ac.destination);
  osc.start(startAt);
  osc.stop(startAt + duration + 0.05);
}
