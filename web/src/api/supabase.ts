import { createClient } from "@supabase/supabase-js";

// PUBLIC by design — the publishable key is meant to be visible client-side.
const url = import.meta.env.VITE_SUPABASE_URL as string | undefined;
// PUBLISHABLE_KEY is the current name (Supabase 2025); ANON_KEY is the
// legacy fallback for pre-rename .env.local files.
const publishableKey =
  (import.meta.env.VITE_SUPABASE_PUBLISHABLE_KEY as string | undefined) ??
  (import.meta.env.VITE_SUPABASE_ANON_KEY as string | undefined);

if (!url || !publishableKey) {
  // Still export a working client so the UI renders; auth calls fail at
  // runtime and the login page explains why.
  console.warn(
    "Supabase env vars missing: set VITE_SUPABASE_URL and VITE_SUPABASE_PUBLISHABLE_KEY in web/.env.local",
  );
}

export const supabase = createClient(url ?? "http://localhost", publishableKey ?? "missing");

export const supabaseConfigured = Boolean(url && publishableKey);
