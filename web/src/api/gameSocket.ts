import type {
  Game,
  GameEventRow,
  GameRatings,
  Message,
  MoveResponse,
  WsEvent,
} from "./types";

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
type RatedListener = (ratings: GameRatings) => void;

const BASE_DELAY_MS = 500;
const MAX_DELAY_MS = 15_000;
const MAX_RECONNECT_ATTEMPTS = 10;
// Grace before tearing down a socket whose last subscriber unmounted, so a
// StrictMode double-mount re-attaches without churning the connection.
const CLEANUP_GRACE_MS = 250;

class GameSocket {
  private readonly gameId: string;
  private ws: WebSocket | null = null;
  private listeners = new Set<Listener>();
  private chatListeners = new Set<ChatListener>();
  private presenceListeners = new Set<PresenceListener>();
  private moveListeners = new Set<MoveListener>();
  private ratedListeners = new Set<RatedListener>();
  private reconnectTimer: number | null = null;
  private closed = false;
  /** Lets new subscribers catch up to the latest presence without waiting
   *  for a fresh event. */
  private presence = new Map<number, boolean>();
  private helloToken: string | null = null;
  /** Highest applied per-game event seq. On reconnect we fetch
   *  /events?since=lastSeq; live events with seq <= lastSeq are skipped to
   *  avoid double-application. */
  private lastSeq: number | null = null;
  /** Serializes message processing so a catch-up fetch triggered by one
   *  event is awaited before the next event in a burst is applied. */
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

  /** Seat token to send on `hello` after each (re)connect; null = spectator. */
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
    // Replay last-known presence so new subscribers don't start blank.
    for (const [seat, online] of this.presence) fn(seat, online);
    return () => this.presenceListeners.delete(fn);
  }

  subscribeMove(fn: MoveListener): () => void {
    this.moveListeners.add(fn);
    return () => this.moveListeners.delete(fn);
  }

  subscribeRated(fn: RatedListener): () => void {
    this.ratedListeners.add(fn);
    return () => this.ratedListeners.delete(fn);
  }

  hasSubscribers(): boolean {
    return (
      this.listeners.size > 0 ||
      this.chatListeners.size > 0 ||
      this.presenceListeners.size > 0 ||
      this.moveListeners.size > 0 ||
      this.ratedListeners.size > 0
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
      // Server sends a fresh state snapshot on connect; just reset attempts.
      this.setState({ status: "open", attempt: 0 });
      // Re-auth the seat so the server cancels its disconnect-grace timer.
      this.sendHello();
    };

    ws.onmessage = (e) => {
      let ev: WsEvent;
      try {
        ev = JSON.parse(e.data as string) as WsEvent;
      } catch {
        return;
      }
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

  /** Dispatch an event to its listener set, isolated from the
   *  sequencing/catch-up bookkeeping in processEvent. */
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
    } else if (ev.type === "rated") {
      for (const l of this.ratedListeners) l(ev.payload);
    }
  }

  /** Process one event with seq tracking + catch-up:
   *   1. seq <= lastSeq: already applied, skip.
   *   2. gap (seq > lastSeq + 1): fetch missing events, then re-check.
   *   3. no gap (or first connect): apply directly.
   *  lastSeq advances after each apply to keep dedup/gap detection current. */
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
        // Catch-up already covered this seq.
        return;
      }
    }

    this.applyEvent(ev);
    if (seq !== undefined) {
      this.lastSeq = seq;
    }
  }

  /** Fetch and apply every event newer than lastSeq. Failure-tolerant:
   *  a blip leaves lastSeq untouched and the next live event retries. The
   *  endpoint caps at 1000 rows; an overflow is picked up by the next fetch. */
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

// Per-game-id singletons so multiple subtrees share one connection and a
// StrictMode unmount/remount doesn't rebuild the socket.
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

// Returns the shared socket, creating it if missing. Caller must arrange at
// least one subscription or the cleanup grace tears it down on the next tick.
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

// Per-move events on the shared socket, for callers needing the captures list.
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

// "rated" events — fired once per game after the server applies the Elo
// update; lets the end-of-game modal swap to final deltas without polling.
export function acquireRatedStream(gameId: string, listener: RatedListener): () => void {
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
  const unsubscribe = socket.subscribeRated(listener);
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

// Per-seat presence events on the shared socket.
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

// Chat events on the shared socket; counts toward the socket's refcount.
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
