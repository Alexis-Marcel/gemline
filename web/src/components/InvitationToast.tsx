import { useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import {
  useInvitations,
  type PendingInvitation,
} from "../notifications/InvitationsProvider";

// InvitationToast renders the pending-invitations stack pinned to the
// bottom-right of the viewport. State lives in InvitationsProvider (so
// the UserNav badge can read the same list); this component is purely
// presentational + dispatches accept / decline to the provider.
//
// Newest invitation sits at the top of the column. Toasts auto-hide
// when the viewer is already on the invited game's page (the inline
// Accepter/Refuser controls take over there).
export function InvitationToast() {
  const { invitations } = useInvitations();
  if (invitations.length === 0) return null;
  return (
    <div className="fixed bottom-4 right-4 z-50 flex w-80 max-w-[90vw] flex-col-reverse gap-2">
      {invitations.map((invite) => (
        <InvitationCard
          key={`${invite.gameId}::${invite.seatIndex}`}
          invite={invite}
        />
      ))}
    </div>
  );
}

function InvitationCard({ invite }: { invite: PendingInvitation }) {
  const { dismiss, decline } = useInvitations();
  const navigate = useNavigate();
  const location = useLocation();
  const [busy, setBusy] = useState(false);

  // Hide the card when the user is already on the invited game's page —
  // the in-page Accepter/Refuser controls handle it. We still keep the
  // entry in the provider's stack so the header badge stays accurate
  // and navigating away brings the card back.
  if (location.pathname === `/game/${invite.gameId}`) return null;

  const hostLabel = invite.fromName?.trim()
    ? invite.fromName.trim()
    : "Quelqu'un";

  async function handleAccept() {
    setBusy(true);
    try {
      navigate(`/game/${invite.gameId}`);
      dismiss(invite.gameId, invite.seatIndex);
    } finally {
      setBusy(false);
    }
  }

  async function handleDecline() {
    setBusy(true);
    try {
      await decline(invite.gameId, invite.seatIndex);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="animate-[slideIn_0.2s_ease-out] rounded-xl border border-amber-500/40 bg-zinc-950/95 p-4 shadow-2xl backdrop-blur"
      role="alert"
      aria-live="polite"
    >
      <p className="text-sm text-zinc-100">
        <span className="font-medium text-amber-300">{hostLabel}</span>{" "}
        t'invite à jouer
      </p>
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
