import { createClient } from "@supabase/supabase-js";

// Env vars are injected by Vite at build time. They are PUBLIC by design
// (the anon key is meant to be visible client-side); the JWT secret never
// leaves the server.
const url = import.meta.env.VITE_SUPABASE_URL as string | undefined;
const anonKey = import.meta.env.VITE_SUPABASE_ANON_KEY as string | undefined;

if (!url || !anonKey) {
  // We still export a working client so the UI can render, but every auth
  // call will fail at runtime — the user sees an explanatory message in the
  // login page. A loud console warning surfaces the misconfiguration in dev.
  console.warn(
    "Supabase env vars missing: set VITE_SUPABASE_URL and VITE_SUPABASE_ANON_KEY in web/.env.local",
  );
}

export const supabase = createClient(url ?? "http://localhost", anonKey ?? "missing");

export const supabaseConfigured = Boolean(url && anonKey);
