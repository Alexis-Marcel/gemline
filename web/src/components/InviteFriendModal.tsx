import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { ProfileSearchEntry } from "../api/types";
import { saveCredentials } from "../lib/auth";
import { Button } from "./Button";

interface InviteFriendModalProps {
  onClose: () => void;
}

const SEARCH_DEBOUNCE_MS = 200;
const PRIVATE_SEATS = 2;

/**
 * InviteFriendModal lets the caller pick another player by name and
 * spawns a fresh private 2-seat game. The caller is seated; the
 * invitee gets the URL via clipboard so they can be reached over
 * whatever channel (Discord, email, etc.). A future revision could
 * push a lobby-WS notification — for now this is the simple path.
 */
export function InviteFriendModal({ onClose }: InviteFriendModalProps) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [results, setResults] = useState<ProfileSearchEntry[]>([]);
  const [searching, setSearching] = useState(false);
  const [inviting, setInviting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Close on Escape — standard modal affordance, mirrors GameEndModal.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Debounced search: 200ms after the user stops typing, fire the
  // query. Anything shorter floods the server with abandoned
  // typeahead requests; anything longer feels laggy.
  useEffect(() => {
    const query = q.trim();
    if (query === "") {
      setResults([]);
      setSearching(false);
      return;
    }
    setSearching(true);
    const handle = window.setTimeout(() => {
      api
        .searchUsers(query)
        .then((r) => setResults(r))
        .catch((err) => {
          setError(err instanceof ApiError ? err.message : "Erreur recherche");
        })
        .finally(() => setSearching(false));
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
  }, [q]);

  async function handlePick(_entry: ProfileSearchEntry) {
    setInviting(true);
    setError(null);
    try {
      // Spawn a fresh 2-seat private game with the caller already
      // seated. We don't pass a name — the server resolves it from
      // the caller's profile (modal is only surfaced to authed
      // users). The invitee joins by URL.
      const res = await api.createGame(PRIVATE_SEATS);
      saveCredentials(res.game.id, {
        token: res.token,
        seatIndex: res.seat.index,
        name: res.seat.name,
      });
      const url = `${window.location.origin}/game/${res.game.id}`;
      try {
        await navigator.clipboard.writeText(url);
      } catch {
        /* Some browsers / contexts disallow clipboard access without a
         * gesture; the user will see the URL in the address bar after
         * the navigate below. */
      }
      navigate(`/game/${res.game.id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur invitation");
    } finally {
      setInviting(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4"
      onClick={onClose}
    >
      <div
        className="relative w-full max-w-md rounded-2xl border border-zinc-800 bg-zinc-950 p-6 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <button
          type="button"
          onClick={onClose}
          className="absolute right-3 top-3 text-zinc-500 hover:text-zinc-200"
          aria-label="Fermer"
        >
          ✕
        </button>

        <header className="mb-4">
          <h2 className="text-lg font-semibold text-zinc-100">
            Inviter un ami
          </h2>
          <p className="mt-1 text-sm text-zinc-400">
            Cherche un joueur par son nom. Tu seras envoyé dans une nouvelle
            partie privée et le lien sera copié dans ton presse-papier.
          </p>
        </header>

        <input
          autoFocus
          type="text"
          placeholder="Nom du joueur…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
        />

        {error && (
          <p className="mt-3 rounded-md border border-red-900/50 bg-red-950/30 p-2 text-sm text-red-300">
            {error}
          </p>
        )}

        <ul className="mt-3 max-h-64 space-y-1 overflow-y-auto">
          {q.trim() === "" && (
            <li className="px-2 py-1 text-xs text-zinc-500">
              Tape les premières lettres d'un nom.
            </li>
          )}
          {q.trim() !== "" && searching && (
            <li className="px-2 py-1 text-xs text-zinc-500">Recherche…</li>
          )}
          {q.trim() !== "" && !searching && results.length === 0 && (
            <li className="px-2 py-1 text-xs text-zinc-500">
              Aucun joueur ne correspond.
            </li>
          )}
          {results.map((e) => (
            <li key={e.userId}>
              <button
                type="button"
                onClick={() => handlePick(e)}
                disabled={inviting}
                className="flex w-full items-center justify-between rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 text-left text-sm transition hover:border-amber-400 disabled:opacity-50"
              >
                <span className="text-zinc-100">{e.displayName}</span>
                <span className="font-mono text-xs tabular-nums text-zinc-400">
                  {e.ratingOneVOne}
                </span>
              </button>
            </li>
          ))}
        </ul>

        <footer className="mt-4 flex justify-end">
          <Button variant="secondary" onClick={onClose}>
            Annuler
          </Button>
        </footer>
      </div>
    </div>
  );
}
