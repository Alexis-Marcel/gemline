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

// Lives in its own module so AuthProvider.tsx can export only the
// React component (fast refresh requires single-export component
// files). useAuth in ./useAuth.ts reads this context.
export const AuthContext = createContext<AuthContextValue | null>(null);
