import type { Game, WsEvent } from "./types";

export type ConnStatus = "connecting" | "open" | "reconnecting" | "offline";

export interface ConnState {
  status: ConnStatus;
  game: Game | null;
  attempt: number;
}

type Listener = (state: ConnState) => void;

const BASE_DELAY_MS = 500;
const MAX_DELAY_MS = 15_000;
const MAX_RECONNECT_ATTEMPTS = 10;
// Grace period before tearing down a socket whose last subscriber unmounted.
// React StrictMode mounts effects twice in dev; this delay lets the re-mount
// re-attach without churning the connection.
const CLEANUP_GRACE_MS = 250;

class GameSocket {
  private readonly gameId: string;
  private ws: WebSocket | null = null;
  private listeners = new Set<Listener>();
  private reconnectTimer: number | null = null;
  private closed = false;
  private state: ConnState = {
    status: "connecting",
    game: null,
    attempt: 0,
  };

  constructor(gameId: string) {
    this.gameId = gameId;
    this.connect();
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    fn(this.state);
    return () => this.listeners.delete(fn);
  }

  hasSubscribers(): boolean {
    return this.listeners.size > 0;
  }

  close(): void {
    this.closed = true;
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.onmessage = null;
      this.ws.onopen = null;
      this.ws.close();
      this.ws = null;
    }
  }

  private setState(patch: Partial<ConnState>): void {
    this.state = { ...this.state, ...patch };
    for (const l of this.listeners) l(this.state);
  }

  private connect(): void {
    if (this.closed) return;
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const url = `${proto}://${window.location.host}/ws/games/${this.gameId}`;
    const ws = new WebSocket(url);
    this.ws = ws;

    ws.onopen = () => {
      // The server sends a fresh state snapshot on connect, so we don't need
      // to manually resync — just reset the attempt counter.
      this.setState({ status: "open", attempt: 0 });
    };

    ws.onmessage = (e) => {
      let ev: WsEvent;
      try {
        ev = JSON.parse(e.data as string) as WsEvent;
      } catch {
        return;
      }
      if (ev.type === "state") {
        this.setState({ game: ev.payload });
      } else if (ev.type === "move") {
        this.setState({ game: ev.payload.game });
      }
    };

    ws.onerror = () => {
      // onerror is always followed by onclose; let onclose handle the retry.
    };

    ws.onclose = () => {
      if (this.closed) return;
      this.ws = null;
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect(): void {
    const attempt = this.state.attempt + 1;
    if (attempt > MAX_RECONNECT_ATTEMPTS) {
      this.setState({ status: "offline", attempt });
      return;
    }
    const base = Math.min(BASE_DELAY_MS * 2 ** (attempt - 1), MAX_DELAY_MS);
    // ±30% jitter so reconnect storms don't synchronize across clients.
    const delay = Math.round(base * (0.7 + Math.random() * 0.6));

    this.setState({ status: "reconnecting", attempt });
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }
}

// Module-level singletons keyed by game id. Multiple React subtrees that need
// the same game share one connection, and a quick unmount/remount (StrictMode)
// doesn't tear down and rebuild the socket.
const sockets = new Map<string, GameSocket>();
const cleanupTimers = new Map<string, number>();

export function acquireSocket(gameId: string, listener: Listener): () => void {
  // Cancel any pending cleanup before re-attaching.
  const pending = cleanupTimers.get(gameId);
  if (pending !== undefined) {
    window.clearTimeout(pending);
    cleanupTimers.delete(gameId);
  }

  let socket = sockets.get(gameId);
  if (!socket) {
    socket = new GameSocket(gameId);
    sockets.set(gameId, socket);
  }

  const unsubscribe = socket.subscribe(listener);

  return () => {
    unsubscribe();
    const s = sockets.get(gameId);
    if (s && !s.hasSubscribers()) {
      const t = window.setTimeout(() => {
        const s2 = sockets.get(gameId);
        if (s2 && !s2.hasSubscribers()) {
          s2.close();
          sockets.delete(gameId);
        }
        cleanupTimers.delete(gameId);
      }, CLEANUP_GRACE_MS);
      cleanupTimers.set(gameId, t);
    }
  };
}

// Test helper: force-close every active socket. Not used in production code.
export function _resetAllSockets(): void {
  for (const t of cleanupTimers.values()) window.clearTimeout(t);
  cleanupTimers.clear();
  for (const s of sockets.values()) s.close();
  sockets.clear();
}
