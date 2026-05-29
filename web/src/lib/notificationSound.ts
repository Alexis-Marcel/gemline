// Notification chime synthesised via the Web Audio API (no asset to ship).
// Browser policy keeps AudioContext suspended until a user gesture: we lazily
// create it on first play and silently do nothing while suspended, resuming
// on a later play once the user has interacted.

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

/** Two-tone "ding-dong". Safe to call repeatedly; async, returns immediately. */
export function playNotificationSound() {
  const ac = getCtx();
  if (!ac) return;
  // resume() isn't awaited — if the user hasn't interacted it rejects and
  // we skip this play.
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
