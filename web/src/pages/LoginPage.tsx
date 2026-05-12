import { useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { supabase, supabaseConfigured } from "../api/supabase";
import { Button } from "../components/Button";

export function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();
  const location = useLocation();
  const next = new URLSearchParams(location.search).get("next") ?? "/";

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    const { error } = await supabase.auth.signInWithPassword({ email, password });
    setSubmitting(false);
    if (error) {
      setError(error.message);
      return;
    }
    navigate(next, { replace: true });
  }

  return (
    <AuthLayout title="Se connecter">
      {!supabaseConfigured && <NotConfiguredNotice />}
      <form onSubmit={handleSubmit} className="space-y-3">
        <Field label="Email" type="email" value={email} onChange={setEmail} autoFocus />
        <Field label="Mot de passe" type="password" value={password} onChange={setPassword} />
        <Button type="submit" disabled={submitting || !supabaseConfigured} className="w-full">
          {submitting ? "Connexion…" : "Se connecter"}
        </Button>
        {error && (
          <p className="rounded-md border border-red-900/50 bg-red-950/30 p-2 text-sm text-red-300">
            {error}
          </p>
        )}
      </form>
      <p className="mt-4 text-center text-sm text-zinc-400">
        Pas de compte ?{" "}
        <Link to="/signup" className="text-amber-400 hover:underline">
          Inscription
        </Link>
      </p>
    </AuthLayout>
  );
}

export function SignupPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setInfo(null);
    setSubmitting(true);
    const { data, error } = await supabase.auth.signUp({
      email,
      password,
      options: { data: { display_name: displayName.trim() || email.split("@")[0] } },
    });
    setSubmitting(false);
    if (error) {
      setError(error.message);
      return;
    }
    if (!data.session) {
      // Email confirmation enabled → no session until they verify.
      setInfo("Vérifie ta boîte mail pour confirmer ton inscription.");
      return;
    }
    navigate("/", { replace: true });
  }

  return (
    <AuthLayout title="Créer un compte">
      {!supabaseConfigured && <NotConfiguredNotice />}
      <form onSubmit={handleSubmit} className="space-y-3">
        <Field label="Nom affiché" type="text" value={displayName} onChange={setDisplayName} autoFocus />
        <Field label="Email" type="email" value={email} onChange={setEmail} />
        <Field label="Mot de passe" type="password" value={password} onChange={setPassword} />
        <Button type="submit" disabled={submitting || !supabaseConfigured} className="w-full">
          {submitting ? "Création…" : "Créer le compte"}
        </Button>
        {error && (
          <p className="rounded-md border border-red-900/50 bg-red-950/30 p-2 text-sm text-red-300">
            {error}
          </p>
        )}
        {info && (
          <p className="rounded-md border border-emerald-900/50 bg-emerald-950/30 p-2 text-sm text-emerald-300">
            {info}
          </p>
        )}
      </form>
      <p className="mt-4 text-center text-sm text-zinc-400">
        Déjà un compte ?{" "}
        <Link to="/login" className="text-amber-400 hover:underline">
          Connexion
        </Link>
      </p>
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

function Field({
  label,
  type,
  value,
  onChange,
  autoFocus,
}: {
  label: string;
  type: string;
  value: string;
  onChange: (v: string) => void;
  autoFocus?: boolean;
}) {
  return (
    <label className="block text-sm text-zinc-400">
      {label}
      <input
        type={type}
        autoFocus={autoFocus}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="mt-1 block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
        required
      />
    </label>
  );
}

function NotConfiguredNotice() {
  return (
    <p className="mb-4 rounded-md border border-amber-900/50 bg-amber-950/30 p-3 text-xs text-amber-200">
      Auth indisponible : variables <code>VITE_SUPABASE_URL</code> et{" "}
      <code>VITE_SUPABASE_PUBLISHABLE_KEY</code> non configurées (voir{" "}
      <code>web/.env.local</code>).
    </p>
  );
}
