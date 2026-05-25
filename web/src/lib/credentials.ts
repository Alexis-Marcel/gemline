// Per-game player credentials, persisted so a page refresh doesn't lose your seat.

import { useEffect, useMemo, useRef, useSyncExternalStore } from "react";

interface Credentials {
  token: string;
  seatIndex: number;
  name: string;
}

const KEY = (gameId: string) => `gemline:auth:${gameId}`;

// In-memory pubsub so React components can react to credential updates
// that happen during a session (e.g. a rematch_ready lobby push saving
// fresh creds while the GamePage is already mounted on the new game).
// localStorage's `storage` event only fires across tabs, not within the
// same one, so we maintain our own subscriber set.
type Listener = (gameId: string) => void;
const listeners = new Set<Listener>();

function emit(gameId: string) {
  for (const fn of listeners) {
    try {
      fn(gameId);
    } catch {
      /* listener error shouldn't break the writer */
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

// useCredentials yields the live seat credentials for `gameId` as React
// state — re-renders whenever saveCredentials/clearCredentials fires for
// the same id. Built on useSyncExternalStore so out-of-band writes (e.g.
// a rematch_ready lobby push landing while the page is already mounted)
// flow through React's scheduling rather than a setState-in-effect dance.
export function useCredentials(gameId: string): Credentials | null {
  // Cache the parsed credentials per game id so useSyncExternalStore's
  // getSnapshot returns referentially-stable values between writes —
  // otherwise loadCredentials would build a fresh object on every call
  // and React would treat each render as a change. The ref outlives any
  // single render so the cache survives re-renders; the effect below
  // resets it when `gameId` changes.
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
