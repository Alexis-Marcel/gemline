import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { authConfigured, signIn, userManager } from "../api/oidc";

// /login and /signup hand off to the account provider (Compte Eriphe), whose
// page offers both sign-in and sign-up; we come back on /auth/callback.
export function LoginPage() {
  const location = useLocation();
  const next = new URLSearchParams(location.search).get("next") ?? "/";
  const [error, setError] = useState<string | null>(null);
  const started = useRef(false);

  useEffect(() => {
    if (!authConfigured || started.current) return;
    started.current = true;
    signIn(next).catch((e: unknown) => setError(e instanceof Error ? e.message : "Connexion impossible"));
  }, [next]);

  return (
    <AuthLayout title="Connexion">
      {!authConfigured ? (
        <NotConfiguredNotice />
      ) : error ? (
        <ErrorNotice message={error} />
      ) : (
        <p className="text-sm text-zinc-400">Redirection vers le Compte Eriphe…</p>
      )}
    </AuthLayout>
  );
}

export const SignupPage = LoginPage;

// Back from the provider: finish the code exchange (PKCE), then go where the
// player was heading.
export function AuthCallbackPage() {
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const done = useRef(false);

  useEffect(() => {
    if (done.current) return;
    done.current = true;
    userManager
      .signinRedirectCallback()
      .then((u) => {
        const next = (u.state as { next?: string } | undefined)?.next;
        navigate(next?.startsWith("/") && !next.startsWith("//") ? next : "/", { replace: true });
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : "Connexion impossible"));
  }, [navigate]);

  return (
    <AuthLayout title="Connexion">
      {error ? <ErrorNotice message={error} /> : <p className="text-sm text-zinc-400">Connexion…</p>}
    </AuthLayout>
  );
}

function AuthLayout({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mx-auto flex h-full max-w-sm flex-col justify-center p-6">
      <Link to="/" className="mb-6 text-sm text-zinc-400 hover:text-zinc-200">
        ← Gemline
      </Link>
      <h1 className="mb-4 text-2xl font-semibold text-zinc-100">{title}</h1>
      {children}
    </div>
  );
}

function ErrorNotice({ message }: { message: string }) {
  return (
    <div className="space-y-3">
      <p className="rounded-md border border-red-900/50 bg-red-950/30 p-2 text-sm text-red-300">{message}</p>
      <Link to="/login" className="text-sm text-amber-400 hover:underline">
        Réessayer
      </Link>
    </div>
  );
}

function NotConfiguredNotice() {
  return (
    <p className="mb-4 rounded-md border border-amber-900/50 bg-amber-950/30 p-3 text-xs text-amber-200">
      Auth indisponible : variables <code>VITE_OIDC_ISSUER</code> et <code>VITE_OIDC_CLIENT_ID</code> non
      configurées (voir <code>web/.env.local</code>).
    </p>
  );
}
