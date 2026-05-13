import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Button } from "../components/Button";
import { UserNav } from "../components/UserNav";
import { saveCredentials } from "../lib/auth";

const MULTIPLAYER_DEFAULT_PLAYERS = 4;

type Mode = "menu" | "private";

export function HomePage() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const [busy, setBusy] = useState<"" | "1v1" | "multi" | "private">("");
  const [joinId, setJoinId] = useState("");
  const [mode, setMode] = useState<Mode>("menu");
  const [privatePlayers, setPrivatePlayers] = useState(2);
  const [error, setError] = useState<string | null>(null);

  async function matchmake(players: number, key: "1v1" | "multi") {
    if (!user) {
      // Matchmaking needs a stable identity (ratings, name). Send anonymous
      // users to login with a return-to so they land back here after.
      navigate("/login?next=/");
      return;
    }
    setBusy(key);
    setError(null);
    try {
      const res = await api.matchmake(players);
      // We're seated atomically — persist the seat token so the GamePage
      // shows us as a player, not a spectator, on first render.
      saveCredentials(res.game.id, {
        token: res.token,
        seatIndex: res.seat.index,
        name: res.seat.name,
      });
      navigate(`/game/${res.game.id}`);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        navigate("/login?next=/");
        return;
      }
      setError(err instanceof ApiError ? err.message : "Erreur matchmaking");
    } finally {
      setBusy("");
    }
  }

  async function handleCreatePrivate(e: FormEvent) {
    e.preventDefault();
    setBusy("private");
    setError(null);
    try {
      const game = await api.createGame(privatePlayers);
      navigate(`/game/${game.id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur création");
    } finally {
      setBusy("");
    }
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
          label="1 contre 1"
          sub="Trouve un adversaire pour un duel."
          onClick={() => matchmake(2, "1v1")}
          loading={busy === "1v1"}
          tone="primary"
        />
        <BigAction
          label="Multijoueur"
          sub={`Jusqu'à ${MULTIPLAYER_DEFAULT_PLAYERS} joueurs, première partie ouverte.`}
          onClick={() => matchmake(MULTIPLAYER_DEFAULT_PLAYERS, "multi")}
          loading={busy === "multi"}
        />
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-500">
          Partie privée
        </h2>
        {mode === "menu" ? (
          <BigAction
            label="Créer une partie privée"
            sub="Tu partages l'URL avec tes invités. Tu peux ajouter des bots sur les sièges vides."
            onClick={() => setMode("private")}
          />
        ) : (
          <form
            onSubmit={handleCreatePrivate}
            className="space-y-3 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4"
          >
            <label className="block text-sm text-zinc-400">
              Nombre de sièges
              <select
                className="mt-1 block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
                value={privatePlayers}
                onChange={(e) => setPrivatePlayers(Number(e.target.value))}
              >
                {[2, 3, 4, 5, 6].map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
            <div className="flex gap-2">
              <Button type="submit" disabled={busy === "private"} className="flex-1">
                {busy === "private" ? "Création…" : "Créer"}
              </Button>
              <Button
                type="button"
                variant="secondary"
                onClick={() => setMode("menu")}
                className="flex-1"
              >
                Annuler
              </Button>
            </div>
          </form>
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
}: {
  label: string;
  sub: string;
  onClick: () => void;
  loading?: boolean;
  tone?: "primary";
}) {
  const base =
    "w-full rounded-xl border px-4 py-3 text-left transition disabled:opacity-50";
  const primary =
    "border-amber-400 bg-amber-400/10 text-amber-100 hover:bg-amber-400/20";
  const neutral =
    "border-zinc-800 bg-zinc-900/40 text-zinc-100 hover:border-zinc-600";
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={loading}
      className={`${base} ${tone === "primary" ? primary : neutral}`}
    >
      <div className="text-base font-medium">
        {loading ? "Recherche…" : label}
      </div>
      <div className="mt-0.5 text-xs text-zinc-400">{sub}</div>
    </button>
  );
}
