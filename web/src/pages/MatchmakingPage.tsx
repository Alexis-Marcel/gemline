import { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { useMatchmake, type MatchmakeState } from "../api/matchmake";
import { useAuth } from "../auth/AuthProvider";
import { saveCredentials } from "../lib/auth";

const MULTIPLAYER_MAX_SEATS = 6;

interface MatchmakingPageProps {
  mode: "1v1" | "multi";
}

/**
 * MatchmakingPage owns the matchmake queue lifecycle for one search session.
 *
 * Lifecycle:
 *   - mount             → matchmake.start(players)
 *   - "matched" event   → save creds + navigate(/game/:id)
 *   - "Annuler" click   → matchmake.cancel() + navigate(/)
 *   - unmount           → the hook fires DELETE /api/matchmake/enqueue
 *
 * Anonymous visitors are bounced to /login since the matchmaking endpoints
 * are auth-gated server-side. The login page's ?next= brings them back.
 *
 * Centralising the queue UI on its own route gives us:
 *   - one owner of useMatchmake, no cross-page races on the userSocket
 *     subscription;
 *   - a clean full-screen UX (no leftover scoreboard / chat / board while
 *     searching);
 *   - a stable URL that survives refresh (the queue row server-side will
 *     reissue match_found once the socket reconnects).
 */
export function MatchmakingPage({ mode }: MatchmakingPageProps) {
  const navigate = useNavigate();
  const { user, loading: authLoading } = useAuth();
  const matchmake = useMatchmake();
  const players = mode === "1v1" ? 2 : MULTIPLAYER_MAX_SEATS;

  // Bounce anon users to login; bring them back here after sign-in.
  useEffect(() => {
    if (authLoading) return;
    if (!user) {
      const next = encodeURIComponent(`/play/${mode}`);
      navigate(`/login?next=${next}`, { replace: true });
    }
  }, [authLoading, user, mode, navigate]);

  // Enter the queue on mount. matchmake.start is stable (useCallback) so
  // this only fires once per page mount.
  useEffect(() => {
    if (authLoading || !user) return;
    void matchmake.start(players);
    // We intentionally omit matchmake.start from deps — it's a stable
    // reference and we only want this on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [authLoading, user, players]);

  // React to terminal states: navigate on match, surface errors on failure.
  useEffect(() => {
    if (matchmake.state.status === "matched") {
      const { match } = matchmake.state;
      saveCredentials(match.gameId, {
        token: match.token,
        seatIndex: match.seatIndex,
        name: match.name,
      });
      navigate(`/game/${match.gameId}`, { replace: true });
    }
  }, [matchmake.state, navigate]);

  async function handleCancel() {
    await matchmake.cancel();
    navigate("/", { replace: true });
  }

  if (authLoading || !user) {
    return (
      <div className="flex h-screen items-center justify-center bg-zinc-950">
        <span className="text-sm text-zinc-400">…</span>
      </div>
    );
  }

  return (
    <div className="flex h-screen items-center justify-center bg-zinc-950 p-6">
      <div className="w-full max-w-sm space-y-6 rounded-2xl border border-zinc-800 bg-zinc-900/60 p-8 text-center shadow-2xl">
        <div
          aria-hidden
          className="mx-auto h-10 w-10 animate-spin rounded-full border-2 border-zinc-700 border-t-amber-400"
        />
        <div className="space-y-1.5">
          <h1 className="text-lg font-medium text-zinc-100">
            {mode === "1v1"
              ? "Recherche d'un adversaire…"
              : "Salle d'attente multijoueur"}
          </h1>
          <p className="text-sm text-zinc-400">{statusLine(matchmake.state, mode)}</p>
        </div>
        {matchmake.state.status === "error" ? (
          <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
            {matchmake.state.message}
          </p>
        ) : null}
        <button
          type="button"
          onClick={handleCancel}
          className="text-sm text-zinc-400 underline-offset-2 transition hover:text-zinc-200 hover:underline"
        >
          Annuler
        </button>
      </div>
    </div>
  );
}

function statusLine(state: MatchmakeState, mode: "1v1" | "multi"): string {
  if (state.status === "queueing") {
    return "Mise en file…";
  }
  if (state.status !== "queued") {
    return mode === "1v1"
      ? "On te trouve un adversaire."
      : "On accumule 3 à 6 joueurs.";
  }
  if (mode === "1v1") {
    const count = state.progress?.count;
    if (count == null || count <= 1) {
      return "On te trouve un adversaire.";
    }
    return `${count} joueurs en file. Pairing par classement en cours…`;
  }
  const count = state.progress?.count ?? 1;
  const eta = state.progress?.etaSeconds;
  const label = `${count}/${MULTIPLAYER_MAX_SEATS} joueurs en attente`;
  if (count < 3) {
    return `${label} — il faut au moins 3 pour démarrer.`;
  }
  if (eta == null) {
    return label;
  }
  if (eta <= 1) {
    return `${label} — démarrage imminent…`;
  }
  return `${label} — démarre dans ${eta}s`;
}
