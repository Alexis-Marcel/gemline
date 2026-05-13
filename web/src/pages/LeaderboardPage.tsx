import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { LeaderboardEntry, RatingMode } from "../api/types";
import { UserNav } from "../components/UserNav";

export function LeaderboardPage() {
  const [mode, setMode] = useState<RatingMode>("1v1");
  const [entries, setEntries] = useState<LeaderboardEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setEntries(null);
    setError(null);
    api
      .getLeaderboard(mode, 50)
      .then((list) => {
        if (!cancelled) setEntries(list);
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof ApiError ? err.message : "Erreur classement");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [mode]);

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <header className="flex items-center justify-between">
        <Link to="/" className="text-lg font-semibold text-zinc-100 hover:text-amber-400">
          Gemline
        </Link>
        <UserNav />
      </header>

      <section className="space-y-3">
        <h1 className="text-2xl font-semibold text-zinc-100">Classement</h1>
        <p className="text-sm text-zinc-400">
          Top 50 par Elo. Les parties privées et les parties avec des bots ne
          comptent pas. 1v1 et multi ont chacun leur classement.
        </p>
        <div className="inline-flex rounded-lg border border-zinc-800 bg-zinc-900/60 p-1 text-sm">
          <ModeToggle current={mode} onChange={setMode} value="1v1" label="1 contre 1" />
          <ModeToggle current={mode} onChange={setMode} value="multi" label="Multijoueur" />
        </div>
      </section>

      {error && (
        <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
          {error}
        </p>
      )}

      {entries === null ? (
        <p className="text-sm text-zinc-400">Chargement…</p>
      ) : entries.length === 0 ? (
        <p className="rounded-md border border-zinc-800 bg-zinc-900/40 p-4 text-sm text-zinc-400">
          {mode === "1v1"
            ? "Personne n'a encore joué de 1v1 public. Sois le premier !"
            : "Personne n'a encore joué de multijoueur public. Sois le premier !"}
        </p>
      ) : (
        <ol className="space-y-1.5">
          {entries.map((e, i) => (
            <li key={e.userId}>
              <div className="flex items-center justify-between rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 text-sm">
                <div className="flex items-center gap-3 min-w-0">
                  <span className={`w-6 text-right font-mono ${rankColor(i)}`}>
                    {i + 1}
                  </span>
                  <span className="truncate text-zinc-100">{e.displayName}</span>
                </div>
                <div className="flex items-center gap-4 text-xs text-zinc-400">
                  <span>
                    {e.wins}W · {e.losses}L · {e.draws}D
                  </span>
                  <span className="font-mono text-base font-semibold text-amber-300">
                    {e.rating}
                  </span>
                </div>
              </div>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

function ModeToggle({
  current,
  onChange,
  value,
  label,
}: {
  current: RatingMode;
  onChange: (m: RatingMode) => void;
  value: RatingMode;
  label: string;
}) {
  const active = current === value;
  return (
    <button
      type="button"
      onClick={() => onChange(value)}
      className={
        "rounded-md px-3 py-1.5 text-xs font-medium transition " +
        (active
          ? "bg-amber-400 text-zinc-950"
          : "text-zinc-300 hover:text-zinc-100")
      }
      aria-pressed={active}
    >
      {label}
    </button>
  );
}

function rankColor(index: number): string {
  if (index === 0) return "text-amber-300";
  if (index === 1) return "text-zinc-300";
  if (index === 2) return "text-amber-700";
  return "text-zinc-500";
}
