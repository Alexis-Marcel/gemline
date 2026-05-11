import type { Game, Message, WsEvent } from "./types";

export type ConnStatus = "connecting" | "open" | "reconnecting" | "offline";

export interface ConnState {
  status: ConnStatus;
  game: Game | null;
  attempt: number;
}

type Listener = (state: ConnState) => void;
type ChatListener = (msg: Message) => void;
type PresenceListener = (seatIndex: number, online: boolean) => void;

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
  private chatListeners = new Set<ChatListener>();
  private presenceListeners = new Set<PresenceListener>();
  private reconnectTimer: number | null = null;
  private closed = false;
  /** Locally-tracked presence map (seatIndex → online). Lets new subscribers
   *  catch up with the latest known state without waiting for a fresh event. */
  private presence = new Map<number, boolean>();
  /** Token sent on every connect for the hello handshake. Set via setHelloToken. */
  private helloToken: string | null = null;
  private state: ConnState = {
    status: "connecting",
    game: null,
    attempt: 0,
  };

  constructor(gameId: string) {
    this.gameId = gameId;
    this.connect();
  }

  /** Configure the seat token to send on `hello` after each (re)connect.
   *  Pass null to revert to spectator mode. */
  setHelloToken(token: string | null): void {
    this.helloToken = token;
    if (token && this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.sendHello();
    }
  }

  private sendHello(): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN || !this.helloToken) return;
    this.ws.send(JSON.stringify({ type: "hello", token: this.helloToken }));
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    fn(this.state);
    return () => this.listeners.delete(fn);
  }

  subscribeChat(fn: ChatListener): () => void {
    this.chatListeners.add(fn);
    return () => this.chatListeners.delete(fn);
  }

  subscribePresence(fn: PresenceListener): () => void {
    this.presenceListeners.add(fn);
    // Replay the last-known presence so new subscribers don't start blank.
    for (const [seat, online] of this.presence) fn(seat, online);
    return () => this.presenceListeners.delete(fn);
  }

  hasSubscribers(): boolean {
    return (
      this.listeners.size > 0 ||
      this.chatListeners.size > 0 ||
      this.presenceListeners.size > 0
    );
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
      // Authenticate this connection's seat (if we have a token). Tells the
      // server we're back so the disconnect-grace timer is cancelled.
      this.sendHello();
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
      } else if (ev.type === "chat") {
        for (const l of this.chatListeners) l(ev.payload);
      } else if (ev.type === "presence") {
        const { seatIndex, online } = ev.payload;
        this.presence.set(seatIndex, online);
        for (const l of this.presenceListeners) l(seatIndex, online);
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

// getSocket returns the shared GameSocket for `gameId`, creating it if
// missing. Caller is responsible for arranging at least one subscription so
// the connection is properly refcounted; otherwise the cleanup grace will
// tear it down on the next tick.
export function getSocket(gameId: string): GameSocket {
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
  return socket;
}

// acquirePresenceStream subscribes to per-seat presence events on the shared
// socket. Returns the unsubscribe.
export function acquirePresenceStream(gameId: string, listener: PresenceListener): () => void {
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
  const unsubscribe = socket.subscribePresence(listener);
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

// acquireChatStream subscribes to chat events on the shared socket for
// `gameId`. The lifecycle counts toward the socket's refcount: as long as a
// chat listener is alive, the socket stays open. Returns the unsubscribe.
export function acquireChatStream(gameId: string, listener: ChatListener): () => void {
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

  const unsubscribe = socket.subscribeChat(listener);

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
