// Persistent per-user WebSocket on /ws/lobby, carrying cross-page
// notifications (match_found, invite_received/cancelled, ...). A singleton
// outside React so any number of subscribers share one connection;
// AuthProvider owns the open/close lifecycle.
//
// The browser can't set Authorization on a WS upgrade, so the Supabase
// JWT travels as ?access_token=.

export type LobbyEventType =
  | "match_found"
  | "invite_received"
  | "invite_cancelled"
  | "queue_update";

export interface MatchFoundPayload {
  gameId: string;
  token: string;
  seatIndex: number;
  name: string;
}

export interface InvitePayload {
  gameId: string;
  seatIndex: number;
  fromName?: string;
  fromUserId?: string;
}

export interface QueueUpdatePayload {
  /** Player-count bucket the caller is queued in (2 for 1v1, 6 for multi). */
  players: number;
  /** Number of users currently waiting in that bucket. */
  count: number;
  /** Seconds until a multi room of the current size auto-starts. Omitted
   *  for 1v1 and for under-quorum multi (no deterministic countdown). */
  etaSeconds?: number;
}

export type LobbyEvent =
  | { type: "match_found"; payload: MatchFoundPayload }
  | { type: "invite_received"; payload: InvitePayload }
  | { type: "invite_cancelled"; payload: InvitePayload }
  | { type: "queue_update"; payload: QueueUpdatePayload };

type Listener = (ev: LobbyEvent) => void;

/** Returns the freshest access token (or null). Called before each
 *  reconnect past the first so a stale token (Supabase refresh missed
 *  while backgrounded) is replaced before burning more attempts. */
export type AuthRefresher = () => Promise<string | null>;

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 15000;

class UserSocket {
  private ws: WebSocket | null = null;
  private listeners = new Set<Listener>();
  private accessToken: string | null = null;
  private reconnectAttempt = 0;
  private reconnectTimer: number | null = null;
  private refreshAuth: AuthRefresher | null = null;
  // true after an explicit close() — suppresses reconnect. open() resets it.
  private closed = true;

  open(accessToken: string) {
    if (this.accessToken === accessToken && this.ws) return;
    // Token rotation: reconnect so the new JWT travels in the query string.
    this.accessToken = accessToken;
    this.closed = false;
    this.clearReconnectTimer();
    this.disconnect();
    this.connect();
  }

  close() {
    this.closed = true;
    this.accessToken = null;
    this.clearReconnectTimer();
    this.disconnect();
  }

  /** The browser WS API doesn't expose the close status code, so we can't
   *  detect a 401 specifically — instead we refresh after every reconnect
   *  past the first (cheap: Supabase no-ops a still-valid token). */
  setAuthRefresher(fn: AuthRefresher | null) {
    this.refreshAuth = fn;
  }

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private connect() {
    if (!this.accessToken) return;
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const url = `${proto}://${window.location.host}/ws/lobby?access_token=${encodeURIComponent(
      this.accessToken,
    )}`;
    const ws = new WebSocket(url);
    this.ws = ws;
    ws.onopen = () => {
      this.reconnectAttempt = 0;
    };
    ws.onmessage = (e) => {
      let ev: LobbyEvent;
      try {
        ev = JSON.parse(e.data as string) as LobbyEvent;
      } catch {
        return;
      }
      for (const fn of this.listeners) {
        try {
          fn(ev);
        } catch {
          // listener error shouldn't crash the socket
        }
      }
    };
    ws.onerror = () => {
      // onerror is always followed by onclose; let onclose handle the retry.
    };
    ws.onclose = () => {
      if (this.ws === ws) {
        this.ws = null;
      }
      if (this.closed || !this.accessToken) return;
      this.scheduleReconnect();
    };
  }

  private disconnect() {
    const ws = this.ws;
    if (!ws) return;
    ws.onmessage = null;
    ws.onerror = null;
    ws.onopen = null;
    ws.onclose = null;
    try {
      ws.close();
    } catch {
      // already closing
    }
    this.ws = null;
  }

  private scheduleReconnect() {
    this.clearReconnectTimer();
    const delay = Math.min(
      RECONNECT_MAX_MS,
      RECONNECT_BASE_MS * Math.pow(2, this.reconnectAttempt),
    );
    const attempt = this.reconnectAttempt;
    this.reconnectAttempt += 1;
    this.reconnectTimer = window.setTimeout(async () => {
      this.reconnectTimer = null;
      // After the first failure, refresh the token (guards against the tab
      // sleeping past a Supabase rotation). A null result means signed out
      // mid-retry — abandon the chain until open() is called again.
      if (attempt > 0 && this.refreshAuth) {
        try {
          const fresh = await this.refreshAuth();
          if (this.closed) return;
          if (fresh == null) {
            this.accessToken = null;
            return;
          }
          this.accessToken = fresh;
        } catch {
          // Refresh failed; try the existing token, refresh again next time.
        }
      }
      this.connect();
    }, delay);
  }

  private clearReconnectTimer() {
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }
}

export const userSocket = new UserSocket();
