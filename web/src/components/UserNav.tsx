import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useAuth } from "../auth/useAuth";
import { useInvitations } from "../notifications/useInvitations";

/**
 * UserNav renders the right-hand "who am I" affordance on top-level pages.
 * Logged out → "Se connecter" link. Logged in → profile avatar plus an
 * invitations bell that badges the count of pending seat invitations and
 * opens a small dropdown listing them (Accepter / Refuser inline).
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
    <div className="flex items-center gap-3">
      <InvitationsBell />
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
    </div>
  );
}

// InvitationsBell renders a bell icon with a count badge when one or
// more seat invitations are pending. Clicking it toggles a dropdown
// that lists each invitation with Accepter/Refuser actions — same
// affordances as the InvitationToast but discoverable after the toast
// has scrolled off. Hidden entirely when the user has no invites so
// it doesn't clutter the header in the steady state.
function InvitationsBell() {
  const { invitations, dismiss, decline } = useInvitations();
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();

  if (invitations.length === 0) return null;

  function handleAccept(gameId: string, seatIndex: number) {
    navigate(`/game/${gameId}`);
    dismiss(gameId, seatIndex);
    setOpen(false);
  }

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="relative grid h-8 w-8 place-items-center rounded-full border border-zinc-800 bg-zinc-900 text-zinc-300 transition hover:border-amber-400 hover:text-amber-300"
        aria-label={`${invitations.length} invitation${invitations.length > 1 ? "s" : ""} en attente`}
      >
        <svg
          aria-hidden
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth={1.8}
          strokeLinecap="round"
          strokeLinejoin="round"
          className="h-4 w-4"
        >
          <path d="M6 8a6 6 0 1 1 12 0c0 7 3 7 3 9H3c0-2 3-2 3-9Z" />
          <path d="M10 21a2 2 0 0 0 4 0" />
        </svg>
        <span className="absolute -right-1 -top-1 grid h-4 w-4 place-items-center rounded-full bg-amber-400 text-[10px] font-semibold text-zinc-950">
          {invitations.length > 9 ? "9+" : invitations.length}
        </span>
      </button>

      {open && (
        <div
          className="absolute right-0 top-full mt-2 w-72 rounded-xl border border-zinc-800 bg-zinc-950/95 p-2 shadow-2xl backdrop-blur"
          role="dialog"
        >
          <p className="px-2 py-1 text-[11px] uppercase tracking-wider text-zinc-500">
            Invitations
          </p>
          <ul className="space-y-1">
            {invitations.map((inv) => {
              const label = inv.fromName?.trim() ? inv.fromName.trim() : "Quelqu'un";
              return (
                <li
                  key={`${inv.gameId}::${inv.seatIndex}`}
                  className="rounded-md p-2 hover:bg-zinc-900/60"
                >
                  <p className="text-sm text-zinc-100">
                    <span className="font-medium text-amber-300">{label}</span>{" "}
                    t'invite
                  </p>
                  <div className="mt-2 flex gap-2">
                    <button
                      type="button"
                      onClick={() => handleAccept(inv.gameId, inv.seatIndex)}
                      className="flex-1 rounded-md bg-amber-400 px-2 py-1 text-xs font-medium text-zinc-950 transition hover:bg-amber-300"
                    >
                      Accepter
                    </button>
                    <button
                      type="button"
                      onClick={() => decline(inv.gameId, inv.seatIndex)}
                      className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-300 transition hover:border-red-400 hover:text-red-300"
                    >
                      Refuser
                    </button>
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}
