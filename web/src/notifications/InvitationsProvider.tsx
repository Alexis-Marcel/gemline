// Shared invitation state for the authenticated user. Owns a stack of
// pending invitations sourced from the persistent userSocket, plus the
// HTTP actions to dismiss one (accept = navigate, decline = POST). Two
// consumers today:
//
//   - InvitationToast renders the stack as a column of banners.
//   - UserNav renders a count badge.
//
// We keep a list rather than a single current invite so a quick burst
// of invites doesn't clobber the earlier ones the user hadn't reacted
// to yet. Each entry is keyed by (gameId, seatIndex) so duplicates
// (server re-publishes on reconnect, etc.) coalesce instead of stacking.

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/useAuth";
import { saveCredentials } from "../lib/credentials";
import { playNotificationSound } from "../lib/notificationSound";
import { userSocket } from "../api/userSocket";
import {
  InvitationsContext,
  type InvitationsContextValue,
  type PendingInvitation,
} from "./InvitationsContext";

function inviteKey(gameId: string, seatIndex: number) {
  return `${gameId}::${seatIndex}`;
}

export function InvitationsProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const [invitations, setInvitations] = useState<PendingInvitation[]>([]);

  // Reset the stack on sign-out — the persistent socket also closes on
  // logout, but state is per-user and shouldn't survive an auth change.
  // Implemented as derived state (compare prev user, reset during
  // render) rather than an effect so the empty stack is visible on the
  // same render that observed the sign-out, not the one after.
  const [prevUserId, setPrevUserId] = useState(user?.id ?? null);
  const currUserId = user?.id ?? null;
  if (prevUserId !== currUserId) {
    setPrevUserId(currUserId);
    if (!currUserId) setInvitations([]);
  }

  // Wire the lobby socket:
  //  - invite_received pushes onto the stack;
  //  - invite_cancelled removes from it (host withdrew the offer while
  //    the toast was still visible);
  //  - rematch_ready saves the fresh seat creds the server issued for
  //    a rematch game pre-seating this user. We piggyback this here
  //    rather than spinning a dedicated provider — same lobby socket,
  //    same per-user lifetime, and the only side-effect we need is the
  //    localStorage write (GamePage picks the change up via the
  //    subscribeCredentials hook).
  useEffect(() => {
    return userSocket.subscribe((ev) => {
      if (ev.type === "invite_received") {
        playNotificationSound();
        setInvitations((prev) => {
          const key = inviteKey(ev.payload.gameId, ev.payload.seatIndex);
          const without = prev.filter(
            (i) => inviteKey(i.gameId, i.seatIndex) !== key,
          );
          return [...without, { ...ev.payload, receivedAt: Date.now() }];
        });
      } else if (ev.type === "invite_cancelled") {
        setInvitations((prev) =>
          prev.filter(
            (i) =>
              !(
                i.gameId === ev.payload.gameId &&
                i.seatIndex === ev.payload.seatIndex
              ),
          ),
        );
      } else if (ev.type === "rematch_ready") {
        saveCredentials(ev.payload.gameId, {
          token: ev.payload.token,
          seatIndex: ev.payload.seatIndex,
          name: ev.payload.name,
        });
      }
    });
  }, []);

  const dismiss = useCallback((gameId: string, seatIndex: number) => {
    setInvitations((prev) =>
      prev.filter((i) => !(i.gameId === gameId && i.seatIndex === seatIndex)),
    );
  }, []);

  const decline = useCallback(
    async (gameId: string, seatIndex: number) => {
      // Optimistic dismiss — even if the API call fails (host already
      // cancelled, network blip), the local stack stays clean. The
      // server side is the source of truth for the seat itself.
      dismiss(gameId, seatIndex);
      try {
        await api.declineSeatInvite(gameId, seatIndex);
      } catch {
        /* swallow — best-effort */
      }
    },
    [dismiss],
  );

  const value = useMemo<InvitationsContextValue>(
    () => ({ invitations, dismiss, decline }),
    [invitations, dismiss, decline],
  );

  return (
    <InvitationsContext.Provider value={value}>
      {children}
    </InvitationsContext.Provider>
  );
}
