import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { Button } from "../components/Button";
import { UserNav } from "../components/UserNav";

export function HomePage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [joinId, setJoinId] = useState("");
  const [players, setPlayers] = useState(2);
  const [error, setError] = useState<string | null>(null);

  async function handleCreate(e: FormEvent) {
    e.preventDefault();
    setCreating(true);
    setError(null);
    try {
      const game = await api.createGame(players);
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

      {error && (
        <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
          {error}
        </p>
      )}
    </div>
  );
}
