import { useEffect, useState } from "react";
import { acquireSocket, type ConnState } from "./gameSocket";

const INITIAL: ConnState = { status: "connecting", game: null, attempt: 0 };

/** Subscribes the component to the shared game socket (see acquireSocket). */
export function useGameSocket(gameId: string | null): ConnState {
  const [state, setState] = useState<ConnState>(INITIAL);

  useEffect(() => {
    if (!gameId) return;
    return acquireSocket(gameId, setState);
  }, [gameId]);

  // Derive INITIAL when there's no game id rather than resetting in the
  // effect, keeping it pure (no setState-in-effect).
  return gameId ? state : INITIAL;
}
