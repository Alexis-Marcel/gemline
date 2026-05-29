import { createContext } from "react";
import type { Session, User } from "@supabase/supabase-js";

export interface AuthContextValue {
  user: User | null;
  session: Session | null;
  loading: boolean;
  /** Bearer JWT for our backend, or null if no session. */
  jwt: string | null;
  signOut: () => Promise<void>;
}

// Separate module so AuthProvider.tsx exports only the component
// (fast-refresh requires single-export component files).
export const AuthContext = createContext<AuthContextValue | null>(null);
