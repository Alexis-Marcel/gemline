import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { Session, User } from "@supabase/supabase-js";
import { supabase } from "../api/supabase";
import { userSocket } from "../api/userSocket";

interface AuthContextValue {
  user: User | null;
  session: Session | null;
  loading: boolean;
  /** Bearer JWT for our backend, or null if no session. */
  jwt: string | null;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // Pull the initial session synchronously from localStorage, then
    // subscribe so we react to login / logout / token refresh.
    supabase.auth
      .getSession()
      .then(({ data }) => setSession(data.session))
      .finally(() => setLoading(false));

    const { data } = supabase.auth.onAuthStateChange((_event, s) => {
      setSession(s);
    });
    return () => {
      data.subscription.unsubscribe();
    };
  }, []);

  // Mirror the current session into the persistent user socket. The
  // socket opens on first authenticated render and reconnects in the
  // background if the network blips; it closes on sign-out so a logged-
  // out viewer doesn't keep an authenticated WS hanging around.
  useEffect(() => {
    const token = session?.access_token;
    if (token) {
      userSocket.open(token);
    } else {
      userSocket.close();
    }
  }, [session?.access_token]);

  const value = useMemo<AuthContextValue>(
    () => ({
      user: session?.user ?? null,
      session,
      loading,
      jwt: session?.access_token ?? null,
      signOut: async () => {
        await supabase.auth.signOut();
      },
    }),
    [session, loading],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}
