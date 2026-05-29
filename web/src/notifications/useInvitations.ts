import { useContext } from "react";
import {
  InvitationsContext,
  type InvitationsContextValue,
} from "./InvitationsContext";

// Throws outside <InvitationsProvider> so misuse is loud, not a silent empty list.
export function useInvitations(): InvitationsContextValue {
  const ctx = useContext(InvitationsContext);
  if (!ctx) {
    throw new Error("useInvitations must be used inside <InvitationsProvider>");
  }
  return ctx;
}
