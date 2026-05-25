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
  /** Called with the updated Game DTO returned by each successful action.
   *  The parent typically pipes it into `setLocalGame` so the UI updates
   *  optimistically while the WS state event catches up. */
  onGame: (g: Game) => void;
  /** Called with a user-facing message on failure. Null clears the error. */
  onError: (msg: string | null) => void;
}

/**
 * useInPlayActions bundles the four buttons available to a seated player
 * during a playing game: forfait (resign), propose / accept / decline draw.
 * They share the same shape — POST with the seat token, surface the new
 * DTO, translate ApiError to a localized message — so wrapping them in a
 * single hook keeps the page wiring layer thin.
 *
 * Each handler short-circuits without creds (a spectator clicking should
 * be impossible from the rendered UI anyway, but defensive). handleResign
 * pops a window.confirm before the request; the rest are immediate.
 */
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
