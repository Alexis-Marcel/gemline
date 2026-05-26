import { useContext } from "react";
import {
  InvitationsContext,
  type InvitationsContextValue,
} from "./InvitationsContext";

/**
 * useInvitations reads the lobby-invite stack from <InvitationsProvider>.
 * Throws when called outside the provider so consumer mistakes surface
 * loudly rather than as a silent empty list.
 */
export function useInvitations(): InvitationsContextValue {
  const ctx = useContext(InvitationsContext);
  if (!ctx) {
    throw new Error("useInvitations must be used inside <InvitationsProvider>");
  }
  return ctx;
}
