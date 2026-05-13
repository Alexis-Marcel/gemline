import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { LobbyEntry, Visibility } from "../api/types";
import { Button } from "../components/Button";
import { UserNav } from "../components/UserNav";

export function HomePage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [joinId, setJoinId] = useState("");
  const [players, setPlayers] = useState(2);
  const [visibility, setVisibility] = useState<Visibility>("private");
  const [error, setError] = useState<string | null>(null);

  async function handleCreate(e: FormEvent) {
    e.preventDefault();
    setCreating(true);
    setError(null);
    try {
      const game = await api.createGame(players, visibility);
      navigate(`/game/${game.id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur inconnue");
    } finally {
      setCreating(false);
    }
  }

  function handleJoin(e: FormEvent) {
    e.preventDefault();
    const id = joinId.trim();
    if (!id) return;
    navigate(`/game/${id}`);
  }

  return (
    <div className="mx-auto flex h-full max-w-md flex-col justify-center gap-8 p-6">
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

      <form
        onSubmit={handleCreate}
        className="space-y-3 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4"
      >
        <h2 className="font-medium text-zinc-200">Nouvelle partie</h2>
        <label className="block text-sm text-zinc-400">
          Nombre de joueurs
          <select
            className="mt-1 block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
            value={players}
            onChange={(e) => setPlayers(Number(e.target.value))}
          >
            {[2, 3, 4, 5, 6].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>

        <fieldset className="grid grid-cols-2 gap-2 text-xs">
          <legend className="sr-only">Visibilité</legend>
          <VisibilityChoice
            value="private"
            current={visibility}
            onChange={setVisibility}
            label="Privée"
            hint="Partage l'URL aux invités"
          />
          <VisibilityChoice
            value="public"
            current={visibility}
            onChange={setVisibility}
            label="Publique"
            hint="Visible dans le lobby"
          />
        </fieldset>

        <Button type="submit" disabled={creating} className="w-full">
          {creating ? "Création…" : "Créer"}
        </Button>
      </form>

      <form
        onSubmit={handleJoin}
        className="space-y-3 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4"
      >
        <h2 className="font-medium text-zinc-200">Rejoindre une partie</h2>
        <label className="block text-sm text-zinc-400">
          ID de la partie
          <input
            className="mt-1 block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-zinc-100 focus:border-amber-400 focus:outline-none"
            placeholder="abcdef0123456789"
            value={joinId}
            onChange={(e) => setJoinId(e.target.value)}
          />
        </label>
        <Button type="submit" variant="secondary" className="w-full">
          Aller à la partie
        </Button>
      </form>

      <Lobby />

      {error && (
        <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
          {error}
        </p>
      )}
    </div>
  );
}

function VisibilityChoice({
  value,
  current,
  onChange,
  label,
  hint,
}: {
  value: Visibility;
  current: Visibility;
  onChange: (v: Visibility) => void;
  label: string;
  hint: string;
}) {
  const active = current === value;
  return (
    <button
      type="button"
      onClick={() => onChange(value)}
      className={
        "rounded-md border px-3 py-2 text-left transition " +
        (active
          ? "border-amber-400 bg-amber-400/10 text-amber-100"
          : "border-zinc-700 bg-zinc-950 text-zinc-300 hover:border-zinc-500")
      }
      aria-pressed={active}
    >
      <div className="text-sm font-medium">{label}</div>
      <div className="text-[11px] text-zinc-400">{hint}</div>
    </button>
  );
}

function Lobby() {
  const navigate = useNavigate();
  const [entries, setEntries] = useState<LobbyEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const list = await api.listLobby();
      setEntries(list);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Erreur lobby");
    }
  }, []);

  useEffect(() => {
    refresh();
    // Re-poll every 5s while the home page is mounted — cheap and the user
    // expects fresh game lists. No WS for the lobby (yet).
    const id = window.setInterval(refresh, 5000);
    return () => window.clearInterval(id);
  }, [refresh]);

  return (
    <section className="space-y-2 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="flex items-center justify-between">
        <h2 className="font-medium text-zinc-200">Parties publiques</h2>
        <button
          type="button"
          onClick={refresh}
          className="text-xs text-zinc-400 transition hover:text-zinc-200"
        >
          Rafraîchir
        </button>
      </div>
      {err && (
        <p className="text-xs text-red-300">{err}</p>
      )}
      {entries === null ? (
        <p className="text-xs text-zinc-500">Chargement…</p>
      ) : entries.length === 0 ? (
        <p className="text-xs text-zinc-500">
          Aucune partie publique ouverte. Crée la première !
        </p>
      ) : (
        <ul className="space-y-1.5">
          {entries.map((e) => (
            <li key={e.gameId}>
              <button
                onClick={() => navigate(`/game/${e.gameId}`)}
                className="flex w-full items-center justify-between rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2 text-left transition hover:border-amber-400/60"
              >
                <span className="font-mono text-[11px] text-zinc-400">
                  {e.gameId.slice(0, 8)}…
                </span>
                <span className="text-xs text-zinc-300">
                  {e.seated}/{e.players} joueurs
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
