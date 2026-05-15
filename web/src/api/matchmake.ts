// Matchmaking flow, client side.
//
// The user clicks "find match" → enqueueMatchmake POSTs the ticket.
// The match arrives as a "match_found" event on the persistent
// `userSocket` (managed by AuthProvider, open as long as we're
// authenticated). We subscribe to that stream while the hook is
// queued and translate the event into the local state machine.
//
// The matchmaking lifecycle is no longer tied to a WebSocket lifecycle:
// the socket is always open, only the queue ticket comes and goes.
// `cancel()` calls the HTTP DELETE explicitly; navigating away with a
// pending ticket falls back to the unmount cleanup below.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "./client";
import { playNotificationSound } from "../lib/notificationSound";
import { userSocket, type MatchFoundPayload } from "./userSocket";

export type LobbyMatch = MatchFoundPayload;

/** Live queue snapshot pushed by the server every matcher tick while
 *  the user is queued. count = how many in the bucket; etaSeconds =
 *  seconds until a multi room of that size auto-starts (absent for
 *  1v1 and under-quorum multi). Surfaced so HomePage can render a
 *  live "3/6 joueurs — démarre dans 14s" indicator. */
export interface QueueProgress {
  count: number;
  etaSeconds?: number;
}

export type MatchmakeState =
  | { status: "idle" }
  | { status: "queueing" }
  | { status: "queued"; players: number; progress?: QueueProgress }
  | { status: "matched"; match: LobbyMatch }
  | { status: "error"; message: string };

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

  const cancel = useCallback(async () => {
    try {
      await api.cancelMatchmake();
    } catch {
      // The server's DELETE is idempotent — a failure here usually
      // means we were just matched and the row is gone. Either way
      // we're returning to idle.
    }
    setState({ status: "idle" });
  }, []);

  const start = useCallback(async (players: number) => {
    setState({ status: "queueing" });
    try {
      await api.enqueueMatchmake(players);
      setState({ status: "queued", players });
    } catch (err) {
      const message =
        err instanceof ApiError ? err.message : "Erreur matchmaking";
      setState({ status: "error", message });
    }
  }, []);

  // Listen for match_found + queue_update on the persistent user
  // socket. We subscribe unconditionally so we don't miss the race
  // where the matcher pairs us before `start()` finishes returning.
  useEffect(() => {
    return userSocket.subscribe((ev) => {
      if (ev.type === "match_found") {
        playNotificationSound();
        setState({ status: "matched", match: ev.payload });
        return;
      }
      if (ev.type === "queue_update") {
        // Only meaningful while queued — folding the live count + ETA
        // into the state lets HomePage render a live spinner. If a
        // tick lands while we're idle (race after a cancel), drop it.
        setState((prev) => {
          if (prev.status !== "queued") return prev;
          if (prev.players !== ev.payload.players) return prev;
          return {
            ...prev,
            progress: {
              count: ev.payload.count,
              etaSeconds: ev.payload.etaSeconds,
            },
          };
        });
      }
    });
  }, []);

  // Best-effort cancel if the component unmounts mid-queue. Doesn't
  // touch the socket — it's owned by AuthProvider and stays open for
  // other subscribers (invitation toast etc.).
  useEffect(() => {
    return () => {
      // Fire-and-forget: a queue row left dangling will be cleaned up
      // by the next matcher tick anyway; we just optimise the common
      // navigate-away case.
      void api.cancelMatchmake().catch(() => undefined);
    };
  }, []);

  return { state, start, cancel };
}
