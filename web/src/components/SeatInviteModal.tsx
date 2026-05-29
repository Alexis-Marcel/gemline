import { useEffect, useState } from "react";
import { ApiError, api } from "../api/client";
import type { Game, ProfileSearchEntry } from "../api/types";
import { Button } from "./Button";

interface SeatInviteModalProps {
  gameId: string;
  seatIndex: number;
  // Updates parent local state immediately, without waiting for the WS event.
  onInvited: (game: Game) => void;
  onClose: () => void;
}

const SEARCH_DEBOUNCE_MS = 200;

export function SeatInviteModal({
  gameId,
  seatIndex,
  onInvited,
  onClose,
}: SeatInviteModalProps) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<ProfileSearchEntry[]>([]);
  const [searching, setSearching] = useState(false);
  const [inviting, setInviting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Empty-query reset via the derived-state pattern (instead of effect-body
  // setState, which the React Compiler lint rule flags).
  const trimmedQ = q.trim();
  const [prevTrimmed, setPrevTrimmed] = useState(trimmedQ);
  if (prevTrimmed !== trimmedQ) {
    setPrevTrimmed(trimmedQ);
    if (trimmedQ === "") {
      setResults([]);
      setSearching(false);
    }
  }

  useEffect(() => {
    if (trimmedQ === "") return;
    const handle = window.setTimeout(() => {
      // setSearching lives in the timeout (not the effect body) to satisfy
      // the React Compiler lint rule.
      setSearching(true);
      api
        .searchUsers(trimmedQ)
        .then((r) => setResults(r))
        .catch((err) =>
          setError(err instanceof ApiError ? err.message : "Erreur recherche"),
        )
        .finally(() => setSearching(false));
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
  }, [trimmedQ]);

  async function handlePick(entry: ProfileSearchEntry) {
    setInviting(true);
    setError(null);
    try {
      const game = await api.inviteSeat(
        gameId,
        seatIndex,
        entry.userId,
        entry.displayName,
      );
      onInvited(game);
      onClose();
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
            Inviter un joueur
          </h2>
          <p className="mt-1 text-sm text-zinc-400">
            Cherche un joueur par son nom. Il sera réservé sur ce siège
            jusqu'à ce qu'il rejoigne la partie.
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
