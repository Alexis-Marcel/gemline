import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { serverConfig } from "../api/authConfig";
import { configureOIDC, userManager } from "../api/oidc";
import { useAuth } from "../auth/useAuth";

// /login and /signup hand off to wherever accounts live for this host: the
// OIDC provider, the local account pages (/compte/), or the reverse proxy.
export function LoginPage() {
  const location = useLocation();
  const next = new URLSearchParams(location.search).get("next") ?? "/";
  const { loading, mode, accounts, accountName, signIn } = useAuth();
  const started = useRef(false);

  useEffect(() => {
    if (loading || !accounts || started.current) return;
    started.current = true;
    signIn(next);
  }, [loading, accounts, signIn, next]);

  return (
    <AuthLayout title="Connexion">
      {loading ? null : !accounts ? (
        <p className="text-sm text-zinc-400">
          {mode === "proxy"
            ? "La connexion se fait par le serveur qui héberge Gemline."
            : "Les comptes ne sont pas activés sur ce serveur : on joue en invité."}
        </p>
      ) : (
        <p className="text-sm text-zinc-400">
          {accountName ? `Redirection vers le compte ${accountName}…` : "Redirection vers la page de connexion…"}
        </p>
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
    serverConfig()
      .then(({ auth }) => {
        if (auth.oidc) configureOIDC(auth.oidc.issuer, auth.oidc.clientId);
        const m = userManager();
        if (!m) throw new Error("Connexion OIDC non configurée sur ce serveur");
        return m.signinRedirectCallback();
      })
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
