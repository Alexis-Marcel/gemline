import { createContext } from "react";
import type { InvitePayload } from "../api/userSocket";

export interface PendingInvitation extends InvitePayload {
  /** Wall-clock arrival, used for stable ordering + de-dup. */
  receivedAt: number;
}

export interface InvitationsContextValue {
  invitations: PendingInvitation[];
  /** Drop one entry from the stack (used by toast + badge). Doesn't
   *  call the server — accept/decline do that separately. */
  dismiss: (gameId: string, seatIndex: number) => void;
  /** POST decline-invite then remove from the stack. */
  decline: (gameId: string, seatIndex: number) => Promise<void>;
}

// Separate module so the provider file satisfies react-refresh's
// "only export components" rule.
export const InvitationsContext = createContext<InvitationsContextValue | null>(
  null,
);
