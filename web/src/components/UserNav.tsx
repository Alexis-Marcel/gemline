import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";

/**
 * UserNav renders the right-hand "who am I" affordance on top-level pages.
 * When logged in: a link to the profile showing the user's email initials.
 * When logged out: a "Se connecter" link.
 */
export function UserNav() {
  const { user, loading } = useAuth();

  if (loading) {
    return <span className="text-xs text-zinc-500">…</span>;
  }

  if (!user) {
    return (
      <Link
        to="/login"
        className="text-sm text-zinc-300 hover:text-amber-400"
      >
        Se connecter
      </Link>
    );
  }

  const initials = (user.email ?? "?")
    .split("@")[0]
    .slice(0, 2)
    .toUpperCase();

  return (
    <Link
      to="/profile"
      className="flex items-center gap-2 text-sm text-zinc-200 hover:text-amber-400"
      title={user.email ?? undefined}
    >
      <span className="grid h-7 w-7 place-items-center rounded-full bg-zinc-800 text-xs font-medium text-zinc-200">
        {initials}
      </span>
      <span className="hidden sm:inline">Profil</span>
    </Link>
  );
}
