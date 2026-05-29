import { useCallback } from "react";
import { api, ApiError } from "../api/client";
import type { Game } from "../api/types";

interface Credentials {
  token: string;
  seatIndex: number;
  name: string;
}

interface UseInPlayActionsOpts {
  gameId: string;
  creds: Credentials | null;
  /** Updated DTO from each successful action; fed back optimistically while
   *  the WS state event catches up. */
  onGame: (g: Game) => void;
  /** User-facing failure message; null clears the error. */
  onError: (msg: string | null) => void;
}

// Bundles the seated-player in-play actions (resign + offer/accept/decline
// draw), which share the POST-token / surface-DTO / translate-error shape.
// handleResign confirms first; the rest are immediate.
export function useInPlayActions({ gameId, creds, onGame, onError }: UseInPlayActionsOpts) {
  const handleResign = useCallback(async () => {
    if (!creds) return;
    if (!window.confirm("Abandonner la partie ?")) return;
    onError(null);
    try {
      onGame(await api.resign(gameId, creds.token));
    } catch (err) {
      onError(err instanceof ApiError ? err.message : "Erreur forfait");
    }
  }, [creds, gameId, onError, onGame]);

  const handleOfferDraw = useCallback(async () => {
    if (!creds) return;
    onError(null);
    try {
      onGame(await api.offerDraw(gameId, creds.token));
    } catch (err) {
      onError(err instanceof ApiError ? err.message : "Erreur nul");
    }
  }, [creds, gameId, onError, onGame]);

  const handleAcceptDraw = useCallback(async () => {
    if (!creds) return;
    onError(null);
    try {
      onGame(await api.acceptDraw(gameId, creds.token));
    } catch (err) {
      onError(err instanceof ApiError ? err.message : "Erreur nul");
    }
  }, [creds, gameId, onError, onGame]);

  const handleDeclineDraw = useCallback(async () => {
    if (!creds) return;
    onError(null);
    try {
      onGame(await api.declineDraw(gameId, creds.token));
    } catch (err) {
      onError(err instanceof ApiError ? err.message : "Erreur nul");
    }
  }, [creds, gameId, onError, onGame]);

  return { handleResign, handleOfferDraw, handleAcceptDraw, handleDeclineDraw };
}
