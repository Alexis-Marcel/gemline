import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { PublicProfile } from "../api/types";

/**
 * PublicProfilePage renders /profile/:userId — anyone's profile as
 * a read-only card. No edit form, no email, no history table for
 * privacy reasons: ratings + aggregate counts only. The current user
 * (looking at their own profile via this route) gets the same shape,
 * which is consistent with how the leaderboard already exposes them.
 */
export function PublicProfilePage() {
  const { userId = "" } = useParams();
  const [profile, setProfile] = useState<PublicProfile | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    api
      .getPublicProfile(userId)
      .then((p) => {
        if (!cancelled) setProfile(p);
      })
      .catch((err) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 404) {
          setError("Profil introuvable.");
        } else {
          setError(err instanceof ApiError ? err.message : "Erreur de chargement");
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [userId]);

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <header className="flex items-baseline justify-between">
        <Link to="/" className="text-lg font-semibold text-zinc-100">
          Gemline
        </Link>
        <Link to="/leaderboard" className="text-sm text-zinc-400 hover:text-amber-300">
          Classement →
        </Link>
      </header>

      {loading && (
        <p className="text-sm text-zinc-400">Chargement…</p>
      )}

      {error && (
        <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
          {error}
        </p>
      )}

      {profile && (
        <>
          <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
            <h1 className="text-2xl font-semibold text-zinc-100">
              {profile.displayName}
            </h1>
            <p className="mt-1 text-xs text-zinc-500">Profil public</p>
          </section>

          <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
            <h2 className="mb-3 font-medium text-zinc-200">Classement</h2>
            <dl className="grid grid-cols-2 gap-3">
              <Stat
                label="Elo 1 contre 1"
                value={profile.ratingOneVOne}
                sub={`${profile.gamesOneVOne} ${profile.gamesOneVOne === 1 ? "partie" : "parties"}`}
                accent="text-amber-300"
              />
              <Stat
                label="Elo multi"
                value={profile.ratingMulti}
                sub={`${profile.gamesMulti} ${profile.gamesMulti === 1 ? "partie" : "parties"}`}
                accent="text-amber-300"
              />
            </dl>
          </section>

          <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
            <h2 className="mb-3 font-medium text-zinc-200">Bilan</h2>
            <dl className="grid grid-cols-3 gap-3">
              <Stat label="Victoires" value={profile.won} accent="text-emerald-400" />
              <Stat label="Défaites" value={profile.lost} accent="text-red-400" />
              <Stat label="Nuls" value={profile.draws} accent="text-zinc-300" />
            </dl>
          </section>
        </>
      )}
    </div>
  );
}

function Stat({
  label,
  value,
  sub,
  accent,
}: {
  label: string;
  value: number;
  sub?: string;
  accent?: string;
}) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-zinc-500">{label}</dt>
      <dd className={`text-2xl font-semibold ${accent ?? "text-zinc-100"}`}>
        {value}
      </dd>
      {sub && <dd className="mt-0.5 text-xs text-zinc-500">{sub}</dd>}
    </div>
  );
}
