// Per-game player credentials, persisted so a page refresh doesn't lose your seat.

import { useEffect, useMemo, useRef, useSyncExternalStore } from "react";

interface Credentials {
  token: string;
  seatIndex: number;
  name: string;
}

const KEY = (gameId: string) => `gemline:auth:${gameId}`;

// In-memory pubsub for in-session credential updates (localStorage's
// `storage` event only fires across tabs, not within the same one).
type Listener = (gameId: string) => void;
const listeners = new Set<Listener>();

function emit(gameId: string) {
  for (const fn of listeners) {
    try {
      fn(gameId);
    } catch {
      // listener error shouldn't break the writer
    }
  }
}

export function saveCredentials(gameId: string, creds: Credentials): void {
  localStorage.setItem(KEY(gameId), JSON.stringify(creds));
  emit(gameId);
}

export function loadCredentials(gameId: string): Credentials | null {
  const raw = localStorage.getItem(KEY(gameId));
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Credentials;
  } catch {
    return null;
  }
}

export function clearCredentials(gameId: string): void {
  localStorage.removeItem(KEY(gameId));
  emit(gameId);
}

export function subscribeCredentials(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useCredentials(gameId: string): Credentials | null {
  // Cache the parsed creds so getSnapshot returns referentially-stable
  // values between writes — otherwise a fresh object each call would make
  // React see every render as a change. The effect resets it on gameId change.
  const cacheRef = useRef<Credentials | null>(loadCredentials(gameId));
  useEffect(() => {
    cacheRef.current = loadCredentials(gameId);
  }, [gameId]);
  const store = useMemo(
    () => ({
      subscribe(notify: () => void) {
        return subscribeCredentials((changedId) => {
          if (changedId !== gameId) return;
          cacheRef.current = loadCredentials(gameId);
          notify();
        });
      },
      getSnapshot() {
        return cacheRef.current;
      },
    }),
    [gameId],
  );
  return useSyncExternalStore(store.subscribe, store.getSnapshot);
}
