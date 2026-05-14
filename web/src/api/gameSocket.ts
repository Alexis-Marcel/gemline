import type { Game, GameEventRow, Message, MoveResponse, WsEvent } from "./types";

export type ConnStatus = "connecting" | "open" | "reconnecting" | "offline";

export interface ConnState {
  status: ConnStatus;
  game: Game | null;
  attempt: number;
}

type Listener = (state: ConnState) => void;
type ChatListener = (msg: Message) => void;
type PresenceListener = (seatIndex: number, online: boolean) => void;
type MoveListener = (move: MoveResponse) => void;

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
  private moveListeners = new Set<MoveListener>();
  private reconnectTimer: number | null = null;
  private closed = false;
  /** Locally-tracked presence map (seatIndex → online). Lets new subscribers
   *  catch up with the latest known state without waiting for a fresh event. */
  private presence = new Map<number, boolean>();
  /** Token sent on every connect for the hello handshake. Set via setHelloToken. */
  private helloToken: string | null = null;
  /**
   * Highest per-game event seq this socket has applied. null before the first
   * event ever arrives. On reconnect, the client sends GET /events?since=lastSeq
   * to pull every event that happened while it was offline; live WS events
   * with seq <= lastSeq are skipped to avoid double-application after the
   * catch-up has already covered them.
   */
  private lastSeq: number | null = null;
  /**
   * Serializes message processing. Each incoming WS frame is appended to this
   * chain; any catch-up fetch awaits inside the chain so a burst of live
   * messages arriving during the fetch wait their turn instead of racing
   * past it.
   */
  private processChain: Promise<void> = Promise.resolve();
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

  subscribeMove(fn: MoveListener): () => void {
    this.moveListeners.add(fn);
    return () => this.moveListeners.delete(fn);
  }

  hasSubscribers(): boolean {
    return (
      this.listeners.size > 0 ||
      this.chatListeners.size > 0 ||
      this.presenceListeners.size > 0 ||
      this.moveListeners.size > 0
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
      // Append to the processing chain so a catch-up fetch triggered by
      // one event is awaited before the next event from the burst is
      // applied. The chain is created fresh each WebSocket lifecycle
      // and survives reconnects (its tail is just an already-settled
      // Promise once everything is processed).
      this.processChain = this.processChain.then(() => this.processEvent(ev));
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

  /**
   * Dispatch an event to its specific listener set. The "what does each
   * event type do to local state" decisions live here, isolated from
   * sequencing / catch-up bookkeeping in processEvent.
   */
  private applyEvent(ev: WsEvent): void {
    if (ev.type === "state") {
      this.setState({ game: ev.payload });
    } else if (ev.type === "move") {
      this.setState({ game: ev.payload.game });
      for (const l of this.moveListeners) l(ev.payload);
    } else if (ev.type === "chat") {
      for (const l of this.chatListeners) l(ev.payload);
    } else if (ev.type === "presence") {
      const { seatIndex, online } = ev.payload;
      this.presence.set(seatIndex, online);
      for (const l of this.presenceListeners) l(seatIndex, online);
    }
  }

  /**
   * Process one event with seq tracking + catch-up. Three branches:
   *   1. Event already covered by an earlier catch-up (seq <= lastSeq):
   *      skip. The HTTP catch-up applied the same row already.
   *   2. Gap detected (seq > lastSeq + 1): fetch the missing events
   *      from /events?since=lastSeq, apply each in order, then check if
   *      the current event is still pending or has been absorbed.
   *   3. No gap, or no prior lastSeq (first connect): apply directly.
   *
   * lastSeq advances after each successful apply so subsequent dedup +
   * gap detection have an up-to-date floor.
   */
  private async processEvent(ev: WsEvent): Promise<void> {
    if (this.closed) return;
    const seq = ev.seq;

    if (seq !== undefined && this.lastSeq !== null && seq <= this.lastSeq) {
      return;
    }

    if (seq !== undefined && this.lastSeq !== null && seq > this.lastSeq + 1) {
      await this.fetchCatchup();
      if (this.closed) return;
      if (this.lastSeq !== null && seq <= this.lastSeq) {
        // The catch-up included this seq's row; nothing new to do here.
        return;
      }
    }

    this.applyEvent(ev);
    if (seq !== undefined) {
      this.lastSeq = seq;
    }
  }

  /**
   * Fetch every event newer than lastSeq from the HTTP catch-up endpoint
   * and apply them in order. Tolerant of failures: a network blip or 5xx
   * just leaves lastSeq where it was, and the next live event will
   * trigger another attempt. The catch-up endpoint caps results at 1000
   * rows per call; if a client was offline long enough to overflow that,
   * a second fetch on the next live event picks up where this one left off.
   */
  private async fetchCatchup(): Promise<void> {
    if (this.lastSeq === null) return;
    const since = this.lastSeq;
    let rows: GameEventRow[];
    try {
      const res = await fetch(`/api/games/${this.gameId}/events?since=${since}`);
      if (!res.ok) return;
      rows = (await res.json()) as GameEventRow[];
    } catch {
      return;
    }
    for (const row of rows) {
      if (this.closed) return;
      if (this.lastSeq !== null && row.seq <= this.lastSeq) continue;
      this.applyEvent({ type: row.type, seq: row.seq, payload: row.payload } as WsEvent);
      this.lastSeq = row.seq;
    }
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

// acquireMoveStream subscribes to per-move events on the shared socket.
// Useful when callers need the captures list, not just the resulting state.
export function acquireMoveStream(gameId: string, listener: MoveListener): () => void {
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
  const unsubscribe = socket.subscribeMove(listener);
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
