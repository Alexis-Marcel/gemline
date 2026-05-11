import { useEffect, useState } from "react";
import { acquireSocket, type ConnState } from "./gameSocket";

const INITIAL: ConnState = { status: "connecting", game: null, attempt: 0 };

/**
 * useGameSocket subscribes the calling component to the live event stream of
 * `gameId`. The underlying WebSocket is shared across all subscribers of the
 * same game id (see acquireSocket in gameSocket.ts), and it survives the
 * StrictMode mount/unmount/remount cycle in development.
 */
export function useGameSocket(gameId: string | null): ConnState {
  const [state, setState] = useState<ConnState>(INITIAL);

  useEffect(() => {
    if (!gameId) {
      setState(INITIAL);
      return;
    }
    return acquireSocket(gameId, setState);
  }, [gameId]);

  return state;
}
