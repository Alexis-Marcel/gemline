import { useContext } from "react";
import { AuthContext } from "./AuthContext";

/**
 * useAuth exposes the live session + user from <AuthProvider>. Throws
 * when called outside the provider tree so misuse is loud rather than
 * silently returning null.
 */
export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}
