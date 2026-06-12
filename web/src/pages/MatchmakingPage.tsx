import { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { useMatchmake, type MatchmakeState } from "../api/matchmake";
import { useAuth } from "../auth/useAuth";

const MULTIPLAYER_MAX_SEATS = 6;

interface MatchmakingPageProps {
  mode: "1v1" | "multi";
}

/**
 * MatchmakingPage owns the matchmake queue lifecycle for one search session:
 * start on mount, navigate on "matched", cancel on unmount. A dedicated route
 * keeps a single useMatchmake owner (no cross-page userSocket races) and a
 * stable URL that survives refresh (the poll re-finds the match after a blip).
 * Anonymous visitors are bounced to /login (matchmaking is auth-gated).
 */
export function MatchmakingPage({ mode }: MatchmakingPageProps) {
  const navigate = useNavigate();
  const { user, loading: authLoading } = useAuth();
  const matchmake = useMatchmake();
  const players = mode === "1v1" ? 2 : MULTIPLAYER_MAX_SEATS;

  // Bounce anon users to login; ?next brings them back after sign-in.
  useEffect(() => {
    if (authLoading) return;
    if (!user) {
      const next = encodeURIComponent(`/play/${mode}`);
      navigate(`/login?next=${next}`, { replace: true });
    }
  }, [authLoading, user, mode, navigate]);

  // Enter the queue on mount.
  useEffect(() => {
    if (authLoading || !user) return;
    void matchmake.start(players);
    // matchmake.start is a stable ref; omit it so this only fires on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [authLoading, user, players]);

  useEffect(() => {
    if (matchmake.state.status === "matched") {
      navigate(`/game/${matchmake.state.match.gameId}`, { replace: true });
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
