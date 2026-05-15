import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useMatchmake, type MatchmakeState } from "../api/matchmake";
import { useAuth } from "../auth/AuthProvider";
import { Button } from "../components/Button";
import { UserNav } from "../components/UserNav";
import { saveCredentials } from "../lib/auth";

// Multi rooms open at the engine's max — the matcher decides the
// actual room size (3..6) at start time based on how many people are
// queued.
const MULTIPLAYER_MAX_SEATS = 6;
const PRIVATE_SEATS = 6;

// oneVOneStatus / multiStatus render the live queue indicator under the
// matchmaking buttons while we're queued. They read from the matchmake
// state's `progress` field (populated by queue_update WS events) and
// fall back to a neutral message when no tick has reported yet.
function oneVOneStatus(state: MatchmakeState): string {
  if (state.status !== "queued") {
    return "On te trouve un adversaire. Reste sur cette page.";
  }
  const count = state.progress?.count;
  if (count == null || count <= 1) {
    return "On te trouve un adversaire. Reste sur cette page.";
  }
  return `${count} joueurs en file. Pairing par classement en cours…`;
}

function multiStatus(state: MatchmakeState): string {
  if (state.status !== "queued") {
    return "On accumule 3 à 6 joueurs. Reste sur cette page.";
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

type Mode = "menu" | "private-name";

export function HomePage() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const [busy, setBusy] = useState<"" | "private">("");
  const [joinId, setJoinId] = useState("");
  const [mode, setMode] = useState<Mode>("menu");
  const [hostName, setHostName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const matchmake = useMatchmake();
  // Track which button the user clicked so the spinner attaches to it
  // rather than to both 1v1 and multi at once.
  const [queuedFor, setQueuedFor] = useState<"1v1" | "multi" | null>(null);

  // When the matcher resolves to a match, save the seat credentials and
  // navigate. The hook stays in "matched" state until we leave the page,
  // which is fine — the navigation effectively unmounts this component
  // and the hook's cleanup teardown fires.
  useEffect(() => {
    if (matchmake.state.status === "matched") {
      const { match } = matchmake.state;
      saveCredentials(match.gameId, {
        token: match.token,
        seatIndex: match.seatIndex,
        name: match.name,
      });
      navigate(`/game/${match.gameId}`);
    } else if (matchmake.state.status === "error") {
      setError(matchmake.state.message);
      setQueuedFor(null);
    } else if (matchmake.state.status === "idle") {
      setQueuedFor(null);
    }
  }, [matchmake.state, navigate]);

  async function findMatch(players: number, key: "1v1" | "multi") {
    if (!user) {
      navigate("/login?next=/");
      return;
    }
    setError(null);
    setQueuedFor(key);
    await matchmake.start(players);
  }

  async function cancelMatch() {
    await matchmake.cancel();
    setQueuedFor(null);
  }

  async function createPrivate(name?: string) {
    setBusy("private");
    setError(null);
    try {
      const res = await api.createGame(PRIVATE_SEATS, name);
      saveCredentials(res.game.id, {
        token: res.token,
        seatIndex: res.seat.index,
        name: res.seat.name,
      });
      navigate(`/game/${res.game.id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur création");
    } finally {
      setBusy("");
    }
  }

  function handlePrivateClick() {
    setError(null);
    if (user) {
      // Authenticated — the server pulls the display name from the profile.
      createPrivate();
      return;
    }
    // Anonymous — ask for a name once, here, so the GamePage never has to.
    setMode("private-name");
  }

  function handlePrivateNameSubmit(e: FormEvent) {
    e.preventDefault();
    const name = hostName.trim();
    if (!name) return;
    createPrivate(name);
  }

  function handleJoin(e: FormEvent) {
    e.preventDefault();
    const id = joinId.trim();
    if (!id) return;
    navigate(`/game/${id}`);
  }

  return (
    <div className="mx-auto flex h-full max-w-md flex-col justify-center gap-6 p-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="flex items-center gap-2 text-3xl font-semibold text-zinc-100">
            <span aria-hidden className="inline-block h-5 w-5 rounded-sm bg-amber-400" />
            Gemline
          </h1>
          <p className="mt-1 text-sm text-zinc-400">
            Plateau hexagonal, alignement ou capture pour gagner.
          </p>
        </div>
        <UserNav />
      </header>

      <section className="space-y-3">
        <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-500">
          Jouer en ligne
        </h2>
        <BigAction
          label={queuedFor === "1v1" ? "Recherche en cours…" : "1 contre 1"}
          sub={
            queuedFor === "1v1"
              ? oneVOneStatus(matchmake.state)
              : "Trouve un adversaire pour un duel."
          }
          onClick={() =>
            queuedFor === "1v1" ? cancelMatch() : findMatch(2, "1v1")
          }
          loading={
            queuedFor === "1v1" && matchmake.state.status === "queueing"
          }
          tone={queuedFor === "1v1" ? undefined : "primary"}
          cancellable={queuedFor === "1v1"}
        />
        <BigAction
          label={queuedFor === "multi" ? "Recherche en cours…" : "Multijoueur"}
          sub={
            queuedFor === "multi"
              ? multiStatus(matchmake.state)
              : "3 à 6 joueurs. La partie démarre dès qu'assez de monde est là."
          }
          onClick={() =>
            queuedFor === "multi"
              ? cancelMatch()
              : findMatch(MULTIPLAYER_MAX_SEATS, "multi")
          }
          loading={
            queuedFor === "multi" && matchmake.state.status === "queueing"
          }
          cancellable={queuedFor === "multi"}
        />
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-500">
          Partie privée
        </h2>
        {mode === "private-name" ? (
          <form
            onSubmit={handlePrivateNameSubmit}
            className="space-y-3 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4"
          >
            <label className="block text-sm text-zinc-400">
              Ton nom
              <input
                autoFocus
                className="mt-1 block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
                placeholder="Alice"
                value={hostName}
                onChange={(e) => setHostName(e.target.value)}
                required
              />
            </label>
            <div className="flex gap-2">
              <Button
                type="submit"
                disabled={busy === "private" || !hostName.trim()}
                className="flex-1"
              >
                {busy === "private" ? "Création…" : "Créer"}
              </Button>
              <Button
                type="button"
                variant="secondary"
                onClick={() => {
                  setMode("menu");
                  setHostName("");
                }}
                className="flex-1"
              >
                Annuler
              </Button>
            </div>
          </form>
        ) : (
          <BigAction
            label="Créer une partie privée"
            sub="Tu joues d'abord, puis tu partages l'URL et lances quand tu veux."
            onClick={handlePrivateClick}
            loading={busy === "private"}
          />
        )}
      </section>

      <section className="space-y-2">
        <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-500">
          Rejoindre par ID
        </h2>
        <form onSubmit={handleJoin} className="flex gap-2">
          <input
            className="flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm text-zinc-100 focus:border-amber-400 focus:outline-none"
            placeholder="abcdef0123456789"
            value={joinId}
            onChange={(e) => setJoinId(e.target.value)}
          />
          <Button type="submit" variant="secondary">
            Aller
          </Button>
        </form>
      </section>

      {error && (
        <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
          {error}
        </p>
      )}

      <footer className="pt-2 text-center text-xs text-zinc-500">
        <Link to="/leaderboard" className="hover:text-amber-300">
          Voir le classement →
        </Link>
      </footer>

    </div>
  );
}

function BigAction({
  label,
  sub,
  onClick,
  loading,
  tone,
  cancellable,
}: {
  label: string;
  sub: string;
  onClick: () => void;
  loading?: boolean;
  tone?: "primary";
  cancellable?: boolean;
}) {
  const base =
    "w-full rounded-xl border px-4 py-3 text-left transition disabled:opacity-50";
  const primary =
    "border-amber-400 bg-amber-400/10 text-amber-100 hover:bg-amber-400/20";
  const neutral =
    "border-zinc-800 bg-zinc-900/40 text-zinc-100 hover:border-zinc-600";
  // Cancellable buttons stay enabled (the click cancels the queue);
  // non-cancellable buttons disable while loading so users can't
  // double-click. The label change ("Recherche en cours…") signals
  // state regardless.
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={loading && !cancellable}
      className={`${base} ${tone === "primary" ? primary : neutral}`}
    >
      <div className="text-base font-medium">
        {loading ? "Recherche…" : label}
      </div>
      <div className="mt-0.5 text-xs text-zinc-400">
        {cancellable ? `${sub} — clique pour annuler.` : sub}
      </div>
    </button>
  );
}
