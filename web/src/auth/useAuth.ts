import { useContext } from "react";
import { AuthContext } from "./AuthContext";

// Throws outside <AuthProvider> so misuse is loud rather than silently null.
export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}
