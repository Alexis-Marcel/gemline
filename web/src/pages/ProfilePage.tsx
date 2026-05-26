import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { Profile, UserGame, UserStats } from "../api/types";
import { useAuth } from "../auth/useAuth";
import { Button } from "../components/Button";

export function ProfilePage() {
  const { user, loading, signOut } = useAuth();
  const navigate = useNavigate();

  const [profile, setProfile] = useState<Profile | null>(null);
  const [stats, setStats] = useState<UserStats | null>(null);
  const [history, setHistory] = useState<UserGame[]>([]);
  const [displayName, setDisplayName] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loadingData, setLoadingData] = useState(true);

  // Mark "fetching" as derived state on auth changes instead of
  // setting it in the effect body — the loading flag becomes visible
  // on the same render that observes the auth change.
  const currUserId = user?.id ?? null;
  const [prevUserId, setPrevUserId] = useState(currUserId);
  const [prevLoading, setPrevLoading] = useState(loading);
  if (prevUserId !== currUserId || prevLoading !== loading) {
    setPrevUserId(currUserId);
    setPrevLoading(loading);
    if (!loading && currUserId) setLoadingData(true);
  }

  useEffect(() => {
    if (loading) return;
    if (!user) {
      navigate("/login?next=/profile", { replace: true });
      return;
    }
    let cancelled = false;
    Promise.all([api.getMe(), api.getMyStats(), api.getMyGames()])
      .then(([p, s, g]) => {
        if (cancelled) return;
        setProfile(p);
        setDisplayName(p.displayName);
        setStats(s);
        setHistory(g);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof ApiError ? err.message : "Erreur de chargement");
      })
      .finally(() => {
        if (!cancelled) setLoadingData(false);
      });
    return () => {
      cancelled = true;
    };
  }, [user, loading, navigate]);

  async function handleSave(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setSaving(true);
    try {
      const updated = await api.updateProfile(displayName.trim());
      setProfile(updated);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur");
    } finally {
      setSaving(false);
    }
  }

  async function handleSignOut() {
    await signOut();
    navigate("/");
  }

  if (loading || loadingData) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-zinc-400">
        Chargement…
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <header className="flex items-baseline justify-between">
        <Link to="/" className="text-lg font-semibold text-zinc-100">
          Gemline
        </Link>
        <button
          onClick={handleSignOut}
          className="text-sm text-zinc-400 hover:text-zinc-200"
        >
          Se déconnecter
        </button>
      </header>

      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
        <h2 className="mb-3 font-medium text-zinc-200">Profil</h2>
        <form onSubmit={handleSave} className="space-y-3">
          <label className="block text-sm text-zinc-400">
            Email
            <input
              type="email"
              value={profile?.email ?? ""}
              readOnly
              className="mt-1 block w-full rounded-md border border-zinc-800 bg-zinc-950/50 px-3 py-2 text-zinc-400"
            />
          </label>
          <label className="block text-sm text-zinc-400">
            Nom affiché
            <input
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              className="mt-1 block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
              required
            />
          </label>
          <Button
            type="submit"
            disabled={saving || displayName.trim() === profile?.displayName}
          >
            {saving ? "Enregistrement…" : "Enregistrer"}
          </Button>
        </form>
        {error && (
          <p className="mt-3 text-sm text-red-300">{error}</p>
        )}
      </section>

      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
        <div className="mb-3 flex items-baseline justify-between">
          <h2 className="font-medium text-zinc-200">Statistiques</h2>
          <Link
            to="/leaderboard"
            className="text-xs text-zinc-400 transition hover:text-amber-300"
          >
            Classement →
          </Link>
        </div>
        {stats ? (
          <>
            <dl className="mb-3 grid grid-cols-2 gap-3">
              <Stat label="Elo 1 contre 1" value={stats.ratingOneVOne} accent="text-amber-300" />
              <Stat label="Elo multi" value={stats.ratingMulti} accent="text-amber-300" />
            </dl>
            <dl className="grid grid-cols-2 gap-3">
              <Stat label="Victoires" value={stats.won} accent="text-emerald-400" />
              <Stat label="Défaites" value={stats.lost} accent="text-red-400" />
            </dl>
          </>
        ) : (
          <p className="text-sm text-zinc-400">—</p>
        )}
      </section>

      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
        <h2 className="mb-3 font-medium text-zinc-200">Historique</h2>
        {history.length === 0 ? (
          <p className="text-sm text-zinc-400">Pas encore de partie jouée.</p>
        ) : (
          <ul className="space-y-2">
            {history.map((g) => (
              <li key={g.gameId}>
                <Link
                  to={`/game/${g.gameId}`}
                  className="flex items-center justify-between rounded-md border border-zinc-800 bg-zinc-950/50 px-3 py-2 text-sm transition hover:border-zinc-700"
                >
                  <div className="flex items-center gap-3">
                    <OutcomeBadge outcome={g.outcome} />
                    <code className="text-xs text-zinc-400">{g.gameId.slice(0, 8)}…</code>
                  </div>
                  <span className="text-xs text-zinc-500">
                    {g.moveCount} coups · {new Date(g.updatedAt).toLocaleDateString()}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function Stat({ label, value, accent }: { label: string; value: number; accent?: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-zinc-500">{label}</dt>
      <dd className={`text-2xl font-semibold ${accent ?? "text-zinc-100"}`}>{value}</dd>
    </div>
  );
}

function OutcomeBadge({ outcome }: { outcome: UserGame["outcome"] }) {
  const styles: Record<UserGame["outcome"], string> = {
    won: "bg-emerald-500/20 text-emerald-300",
    lost: "bg-red-500/20 text-red-300",
    draw: "bg-zinc-500/20 text-zinc-300",
    ongoing: "bg-amber-500/20 text-amber-300",
  };
  const labels: Record<UserGame["outcome"], string> = {
    won: "Victoire",
    lost: "Défaite",
    draw: "Nul",
    ongoing: "En cours",
  };
  return (
    <span className={`rounded px-2 py-0.5 text-xs font-medium ${styles[outcome]}`}>
      {labels[outcome]}
    </span>
  );
}
