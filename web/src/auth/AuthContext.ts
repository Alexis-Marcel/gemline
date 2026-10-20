import { createContext } from "react";
import type { AuthUser } from "../api/oidc";

export interface AuthContextValue {
  user: AuthUser | null;
  loading: boolean;
  /** Bearer JWT for our backend (oidc mode), or null. */
  jwt: string | null;
  /** The host's account method: none, oidc, proxy, local. */
  mode: string;
  /** Accounts can be used here (a sign-in button makes sense). */
  accounts: boolean;
  /** Provider name for « Se connecter avec … », empty if the host gives none. */
  accountName: string;
  /** Playing without an account is allowed. */
  guestPlay: boolean;
  /** Account settings page (local mode), empty otherwise. */
  accountUrl: string;
  canSignOut: boolean;
  signIn: (next?: string) => void;
  signOut: () => Promise<void>;
}

// Separate module so AuthProvider.tsx exports only the component
// (fast-refresh requires single-export component files).
export const AuthContext = createContext<AuthContextValue | null>(null);
