import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { Game } from "../api/types";

interface Credentials {
  token: string;
  seatIndex: number;
  name: string;
}

interface UseRematchFlowOpts {
  gameId: string;
  /** Latest known DTO for the original (finished) game. The auto-redirect
   *  effect watches `game.rematchGameId` for the empty → set transition. */
  game: Game | null;
  creds: Credentials | null;
  onGame: (g: Game) => void;
  onError: (msg: string | null) => void;
}

/**
 * Rematch state machine. handleOfferRematch posts propose/accept (server
 * disambiguates); the caller that completes the set gets rematchGameId back
 * and navigates immediately. The other accepters learn of the new game via
 * the WS state event and navigate through handleGoToRematch or the effect
 * below. `rematching` gates the buttons during the roundtrip.
 */
export function useRematchFlow({ gameId, game, creds, onGame, onError }: UseRematchFlowOpts) {
  const navigate = useNavigate();
  const [rematching, setRematching] = useState(false);

  const handleOfferRematch = useCallback(async () => {
    if (!creds) return;
    setRematching(true);
    onError(null);
    try {
      const g = await api.offerRematch(gameId, creds.token);
      onGame(g);
      if (g.rematchGameId) {
        navigate(`/game/${g.rematchGameId}`);
      }
    } catch (err) {
      onError(err instanceof ApiError ? err.message : "Erreur revanche");
    } finally {
      setRematching(false);
    }
  }, [creds, gameId, navigate, onError, onGame]);

  const handleDeclineRematch = useCallback(async () => {
    if (!creds) return;
    setRematching(true);
    onError(null);
    try {
      onGame(await api.declineRematch(gameId, creds.token));
    } catch (err) {
      onError(err instanceof ApiError ? err.message : "Erreur revanche");
    } finally {
      setRematching(false);
    }
  }, [creds, gameId, onError, onGame]);

  // Pull out a primitive dep — `game?.rematchGameId` directly tripped the
  // React Compiler memoization check.
  const rematchGameId = game?.rematchGameId;
  const handleGoToRematch = useCallback(() => {
    if (!rematchGameId) return;
    navigate(`/game/${rematchGameId}`);
  }, [rematchGameId, navigate]);

  // Auto-redirect on the empty → set transition. The first observation is
  // a baseline (not a transition) so loading a finished game whose rematch
  // already existed doesn't auto-kidnap the viewer.
  const lastRematchIdRef = useRef<string | undefined>(undefined);
  const sawRematchRef = useRef(false);
  useEffect(() => {
    if (!game) return;
    const curr = game.rematchGameId;
    if (!sawRematchRef.current) {
      sawRematchRef.current = true;
      lastRematchIdRef.current = curr;
      return;
    }
    const prev = lastRematchIdRef.current;
    lastRematchIdRef.current = curr;
    if (curr && !prev && creds) {
      navigate(`/game/${curr}`);
    }
  }, [game, creds, navigate]);

  return { rematching, handleOfferRematch, handleDeclineRematch, handleGoToRematch };
}
