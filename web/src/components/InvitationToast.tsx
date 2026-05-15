import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { userSocket, type InvitePayload } from "../api/userSocket";

// InvitationToast renders a global "X t'invite à jouer" banner pinned
// to the bottom-right whenever the local user receives an
// invite_received event from the persistent user socket. Two actions:
//   - Accepter → navigate to /game/{gameId}; the GamePage's in-page
//     Accepter/Refuser controls take over (the in-place accept goes
//     through join + pickSeatForUser, which routes to the reserved
//     seat).
//   - Refuser  → POST decline-invite directly from the toast so the
//     invitee never has to navigate just to decline.
//
// The toast also auto-dismisses on invite_cancelled (host withdrew the
// invitation) and hides itself if the user is already on the invited
// game's page (the in-page controls are visible there).
export function InvitationToast() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [invite, setInvite] = useState<InvitePayload | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Subscribe to invite events on the shared socket. We replace any
  // current invitation with the latest — showing a stack of multiple
  // pending invitations is a refinement we don't need yet.
  useEffect(() => {
    return userSocket.subscribe((ev) => {
      if (ev.type === "invite_received") {
        setInvite(ev.payload);
        setError(null);
      } else if (ev.type === "invite_cancelled") {
        setInvite((prev) => {
          if (!prev) return prev;
          if (
            prev.gameId === ev.payload.gameId &&
            prev.seatIndex === ev.payload.seatIndex
          ) {
            return null;
          }
          return prev;
        });
      }
    });
  }, []);

  // Auto-dismiss on logout. If the user signs out while a toast is
  // open, the socket closes and no further events will arrive — but
  // the existing invitation state would stick around without this.
  useEffect(() => {
    if (!user) setInvite(null);
  }, [user]);

  if (!invite || !user) return null;

  // Hide on the invited game's page: the inline buttons are visible
  // there, no need for a duplicate prompt.
  if (location.pathname === `/game/${invite.gameId}`) return null;

  const hostLabel = invite.fromName?.trim()
    ? invite.fromName.trim()
    : "Quelqu'un";

  async function handleAccept() {
    if (!invite) return;
    setBusy(true);
    setError(null);
    try {
      navigate(`/game/${invite.gameId}`);
      setInvite(null);
    } finally {
      setBusy(false);
    }
  }

  async function handleDecline() {
    if (!invite) return;
    setBusy(true);
    setError(null);
    try {
      await api.declineSeatInvite(invite.gameId, invite.seatIndex);
      setInvite(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur invitation");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed bottom-4 right-4 z-50 w-80 max-w-[90vw] rounded-xl border border-amber-500/40 bg-zinc-950/95 p-4 shadow-2xl backdrop-blur"
      role="alert"
      aria-live="polite"
    >
      <p className="text-sm text-zinc-100">
        <span className="font-medium text-amber-300">{hostLabel}</span>{" "}
        t'invite à jouer
      </p>
      {error && (
        <p className="mt-2 text-xs text-red-300">{error}</p>
      )}
      <div className="mt-3 flex gap-2">
        <button
          type="button"
          onClick={handleAccept}
          disabled={busy}
          className="flex-1 rounded-md bg-amber-400 px-3 py-1.5 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
        >
          Accepter
        </button>
        <button
          type="button"
          onClick={handleDecline}
          disabled={busy}
          className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-sm text-zinc-300 transition hover:border-red-400 hover:text-red-300 disabled:opacity-50"
        >
          Refuser
        </button>
      </div>
    </div>
  );
}
