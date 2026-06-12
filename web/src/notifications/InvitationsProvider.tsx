// Shared pending-invitation stack for the authed user, sourced from the
// userSocket plus the HTTP dismiss/decline actions. A list (not a single
// invite) so a burst doesn't clobber earlier ones; entries keyed by
// (gameId, seatIndex) so reconnect re-publishes coalesce.

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/useAuth";
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

  // Reset the stack on sign-out. Derived state (reset during render) so the
  // empty stack shows on the same render that observes the sign-out.
  const [prevUserId, setPrevUserId] = useState(user?.id ?? null);
  const currUserId = user?.id ?? null;
  if (prevUserId !== currUserId) {
    setPrevUserId(currUserId);
    if (!currUserId) setInvitations([]);
  }

  // Lobby socket: invite_received pushes, invite_cancelled removes. Seat
  // credentials are never delivered here — clients pull them via resolveSeat.
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
      // Optimistic dismiss: the local stack stays clean even if the POST
      // fails (host already cancelled, blip); the server owns the seat.
      dismiss(gameId, seatIndex);
      try {
        await api.declineSeatInvite(gameId, seatIndex);
      } catch {
        // best-effort
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
