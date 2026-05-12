import { createClient } from "@supabase/supabase-js";

// Env vars are injected by Vite at build time. They are PUBLIC by design
// (the publishable key is meant to be visible client-side); the JWT secret
// and the Supabase secret-key never leave the server.
const url = import.meta.env.VITE_SUPABASE_URL as string | undefined;
// VITE_SUPABASE_PUBLISHABLE_KEY is the new name (Supabase 2025); the
// legacy VITE_SUPABASE_ANON_KEY is read as a fallback so anyone who set
// up their .env.local before the rename keeps working.
const publishableKey =
  (import.meta.env.VITE_SUPABASE_PUBLISHABLE_KEY as string | undefined) ??
  (import.meta.env.VITE_SUPABASE_ANON_KEY as string | undefined);

if (!url || !publishableKey) {
  // We still export a working client so the UI can render, but every auth
  // call will fail at runtime — the user sees an explanatory message in the
  // login page. A loud console warning surfaces the misconfiguration in dev.
  console.warn(
    "Supabase env vars missing: set VITE_SUPABASE_URL and VITE_SUPABASE_PUBLISHABLE_KEY in web/.env.local",
  );
}

export const supabase = createClient(url ?? "http://localhost", publishableKey ?? "missing");

export const supabaseConfigured = Boolean(url && publishableKey);
