import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { acquireChatStream } from "../api/gameSocket";
import type { Message } from "../api/types";
import { gemColor } from "../lib/colors";
import { Button } from "./Button";

const MAX_LEN = 500;

interface ChatPanelProps {
  gameId: string;
  // Seat token of the current player, or null for spectators.
  playerToken: string | null;
  // Drop the card chrome and fill parent height; used by the mobile drawer.
  embedded?: boolean;
}

export function ChatPanel({ gameId, playerToken, embedded = false }: ChatPanelProps) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  // Initial fetch + live updates from the shared socket.
  useEffect(() => {
    let cancelled = false;

    api
      .getMessages(gameId)
      .then((msgs) => {
        if (!cancelled) setMessages(msgs);
      })
      .catch(() => {
        // non-fatal: chat starts empty
      });

    const unsubscribe = acquireChatStream(gameId, (msg) => {
      setMessages((prev) =>
        prev.some((m) => m.id === msg.id) ? prev : [...prev, msg],
      );
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [gameId]);

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!playerToken) return;
    const body = draft.trim();
    if (!body) return;
    setSending(true);
    setError(null);
    try {
      // Don't add locally — the server broadcasts back and we dedupe by id.
      await api.postMessage(gameId, playerToken, body);
      setDraft("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur d'envoi");
    } finally {
      setSending(false);
    }
  }

  const containerCls = embedded
    ? "flex h-full flex-col bg-zinc-950"
    : "flex flex-col rounded-xl border border-zinc-800 bg-zinc-900/40";
  const listCls = embedded
    ? "flex-1 overflow-y-auto px-3 py-2 text-sm"
    : "max-h-64 min-h-32 flex-1 overflow-y-auto px-3 py-2 text-sm";

  return (
    <section className={containerCls}>
      {!embedded && (
        <header className="border-b border-zinc-800 px-3 py-2">
          <h2 className="text-sm font-medium text-zinc-200">Chat</h2>
        </header>
      )}

      <div ref={scrollRef} className={listCls}>
        {messages.length === 0 ? (
          <p className="text-xs italic text-zinc-500">
            Tu peux discuter avec les autres joueurs ici.
          </p>
        ) : (
          <ul className="space-y-1.5">
            {messages.map((m) => (
              <li key={m.id} className="flex items-start gap-2">
                <span
                  aria-hidden
                  className="mt-1 inline-block h-2 w-2 flex-none rounded-full border border-black/40"
                  style={{ background: gemColor(m.authorColor) ?? "#52525b" }}
                />
                <div className="min-w-0 flex-1">
                  <span className="text-xs font-medium text-zinc-300">
                    {m.authorName}
                  </span>
                  <p className="break-words text-zinc-100">{m.body}</p>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      {playerToken ? (
        <form onSubmit={handleSubmit} className="border-t border-zinc-800 p-2">
          <div className="flex min-w-0 items-stretch gap-2">
            <input
              type="text"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              maxLength={MAX_LEN}
              placeholder="Message…"
              className="min-w-0 flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm text-zinc-100 focus:border-amber-400 focus:outline-none"
              disabled={sending}
            />
            <Button
              type="submit"
              disabled={sending || draft.trim() === ""}
              className="shrink-0 px-3 py-1 text-xs"
            >
              Envoyer
            </Button>
          </div>
          {error && <p className="mt-1 text-xs text-red-300">{error}</p>}
        </form>
      ) : (
        <p className="border-t border-zinc-800 px-3 py-2 text-xs text-zinc-500">
          Rejoins la partie pour participer au chat.
        </p>
      )}
    </section>
  );
}
