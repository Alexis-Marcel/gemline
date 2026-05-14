// Matchmaking flow, client side.
//
// The user clicks "find match" → enqueueMatchmake POSTs the ticket →
// the lobby WS opens → we wait for a "match_found" event → we resolve
// to the matched game ID + seat token so the caller can saveCredentials
// and navigate.
//
// The lobby WS auth is awkward: the browser can't set Authorization on
// a WebSocket upgrade, so we pass the Supabase JWT through
// ?access_token=. The server's authorizationBearer reads it.
//
// We deliberately do NOT use the shared-socket pattern from
// gameSocket.ts: a user has at most one matchmaking session, so a
// simple per-hook lifecycle is fine. No seq tracking either — lobby
// events are ephemeral; a missed match_found is recoverable by
// re-enqueuing.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "./client";
import { supabase } from "./supabase";

export interface LobbyMatch {
  gameId: string;
  token: string;
  seatIndex: number;
  name: string;
}

export type MatchmakeState =
  | { status: "idle" }
  | { status: "queueing" }
  | { status: "queued"; players: number }
  | { status: "matched"; match: LobbyMatch }
  | { status: "error"; message: string };

interface LobbyEvent {
  type: string;
  payload: LobbyMatch;
}

/**
 * useMatchmake exposes a small state machine over the matchmaking
 * flow. The caller renders a button when state.status is "idle", a
 * spinner with a cancel button when "queueing"/"queued", and reacts
 * to "matched" by saving credentials + navigating. State transitions
 * are driven by `start` (enter the queue) and `cancel` (leave it).
 */
export function useMatchmake(): {
  state: MatchmakeState;
  start: (players: number) => Promise<void>;
  cancel: () => Promise<void>;
} {
  const [state, setState] = useState<MatchmakeState>({ status: "idle" });
  const wsRef = useRef<WebSocket | null>(null);

  const teardown = useCallback(() => {
    const ws = wsRef.current;
    if (ws) {
      // Drop handlers before close so the onclose retry path doesn't
      // re-fire after we've already moved on.
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.onopen = null;
      try {
        ws.close();
      } catch {
        /* already closing */
      }
      wsRef.current = null;
    }
  }, []);

  const cancel = useCallback(async () => {
    teardown();
    try {
      await api.cancelMatchmake();
    } catch {
      // The server's DELETE is idempotent — a failure here usually
      // means we were just matched and the row is gone. Either way
      // we're returning to idle.
    }
    setState({ status: "idle" });
  }, [teardown]);

  const start = useCallback(
    async (players: number) => {
      setState({ status: "queueing" });
      try {
        const {
          data: { session },
        } = await supabase.auth.getSession();
        if (!session?.access_token) {
          setState({ status: "error", message: "Connexion requise" });
          return;
        }

        await api.enqueueMatchmake(players);

        const proto = window.location.protocol === "https:" ? "wss" : "ws";
        const url = `${proto}://${window.location.host}/ws/lobby?access_token=${encodeURIComponent(
          session.access_token,
        )}`;
        const ws = new WebSocket(url);
        wsRef.current = ws;

        ws.onmessage = (e) => {
          let ev: LobbyEvent;
          try {
            ev = JSON.parse(e.data as string) as LobbyEvent;
          } catch {
            return;
          }
          if (ev.type === "match_found" && ev.payload) {
            teardown();
            setState({ status: "matched", match: ev.payload });
          }
        };

        ws.onerror = () => {
          // Defer the error surfacing to onclose — onerror is always
          // followed by onclose and we want a single transition.
        };

        ws.onclose = () => {
          // If we haven't already moved past "queued" (i.e. matched),
          // surface this as a soft error so the user knows to retry.
          // Closing intentionally during cancel/teardown nils the
          // handler before close so this branch doesn't fire.
          setState((prev) =>
            prev.status === "matched"
              ? prev
              : { status: "error", message: "Connexion perdue" },
          );
        };

        setState({ status: "queued", players });
      } catch (err) {
        teardown();
        const message =
          err instanceof ApiError
            ? err.message
            : "Erreur matchmaking";
        setState({ status: "error", message });
      }
    },
    [teardown],
  );

  // Clean up if the component unmounts mid-queue.
  useEffect(() => {
    return () => {
      const stillQueued = wsRef.current !== null;
      teardown();
      if (stillQueued) {
        // Fire-and-forget: page is going away, we don't await.
        void api.cancelMatchmake().catch(() => undefined);
      }
    };
  }, [teardown]);

  return { state, start, cancel };
}
