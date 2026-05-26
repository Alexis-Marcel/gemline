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
 * useRematchFlow owns the chess.com-style rematch state machine on the
 * client:
 *
 *  - handleOfferRematch posts the propose/accept call (the server
 *    disambiguates). When this caller's accept completes the unanimous
 *    set, the response carries rematchGameId and we navigate immediately.
 *  - handleDeclineRematch withdraws or refuses an offer; the wire shape
 *    is the same for both — the server doesn't care who's clearing.
 *  - handleGoToRematch is the "Aller à la revanche" affordance for
 *    players who weren't the last to accept (they learn about the new
 *    game through the state event broadcast, not their HTTP response).
 *  - The internal effect handles the *other* accepters who learn about
 *    the new game via WS: when game.rematchGameId flips from empty to
 *    set, navigate them over. A ref tracks the previous value so a
 *    fresh page load on a finished game that already has a rematch
 *    doesn't kidnap the viewer — only a genuine transition triggers.
 *
 * `rematching` is true while any of the propose/accept/decline calls is
 * in flight; RematchControls uses it to disable both buttons during the
 * roundtrip.
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

  // Pull the optional rematchGameId out of `game` into a local before
  // the useCallback so the compiler sees a primitive dep (a string |
  // undefined). The original `game?.rematchGameId` dep tripped the
  // React Compiler memoization check.
  const rematchGameId = game?.rematchGameId;
  const handleGoToRematch = useCallback(() => {
    if (!rematchGameId) return;
    navigate(`/game/${rematchGameId}`);
  }, [rematchGameId, navigate]);

  // Auto-redirect on the empty → set transition. The two refs together
  // model "what value did we last observe?", so the *first* render
  // (where curr is whatever the server already had) is a baseline rather
  // than a transition — otherwise loading a finished game whose rematch
  // existed pre-mount would auto-kidnap the viewer.
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
