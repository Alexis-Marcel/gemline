// Matchmaking flow, client side. enqueueMatchmake POSTs a ticket; the match
// arrives as a "match_found" event on the always-open userSocket (owned by
// AuthProvider). Only the queue ticket comes and goes — cancel() DELETEs it,
// and navigating away falls back to the unmount cleanup below.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "./client";
import { playNotificationSound } from "../lib/notificationSound";
import { userSocket, type MatchFoundPayload } from "./userSocket";

export type LobbyMatch = MatchFoundPayload;

/** Live queue snapshot pushed each matcher tick. count = bucket size;
 *  etaSeconds = until a multi room auto-starts (absent for 1v1 and
 *  under-quorum multi). Drives the live "3/6 joueurs — démarre dans 14s". */
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

/** A small state machine over the matchmaking flow, driven by `start`
 *  (enter the queue) and `cancel` (leave it). */
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
      // DELETE is idempotent; a failure usually means we were just matched.
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

  const markMatched = useCallback((gameId: string) => {
    setState((prev) =>
      prev.status === "matched" ? prev : { status: "matched", match: { gameId } },
    );
  }, []);

  // Subscribe unconditionally so we don't miss the race where the matcher
  // pairs us before `start()` returns. match_found is just a fast navigation
  // hint; the poll below is the durable fallback if it's dropped.
  useEffect(() => {
    return userSocket.subscribe((ev) => {
      if (ev.type === "match_found") {
        markMatched(ev.payload.gameId);
        return;
      }
      if (ev.type === "queue_update") {
        // Only meaningful while queued; drop ticks that land idle (post-cancel race).
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
  }, [markMatched]);

  // Durable fallback: while queued, poll for the game we were matched into in
  // case the match_found push never landed.
  useEffect(() => {
    if (state.status !== "queued") return;
    let cancelled = false;
    const interval = window.setInterval(async () => {
      try {
        const { gameId } = await api.currentMatch();
        if (!cancelled && gameId) markMatched(gameId);
      } catch {
        // transient — the next tick retries
      }
    }, 2500);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [state.status, markMatched]);

  useEffect(() => {
    if (state.status === "matched") playNotificationSound();
  }, [state.status]);

  // Best-effort cancel on unmount mid-queue. Doesn't touch the socket
  // (owned by AuthProvider). Fire-and-forget — a dangling row is reaped
  // by the next matcher tick anyway.
  useEffect(() => {
    return () => {
      void api.cancelMatchmake().catch(() => undefined);
    };
  }, []);

  return { state, start, cancel };
}
