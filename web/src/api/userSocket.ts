// Persistent per-user WebSocket on /ws/lobby. Opens at login, stays
// open across pages, reconnects on transient disconnect. Carries all
// cross-page notifications that aren't tied to a single game:
//   - match_found      — matchmaker paired the user (lobby/matchmake hook)
//   - invite_received  — someone reserved a seat for them in a private game
//   - invite_cancelled — that reservation was withdrawn
//
// The singleton lives outside React so subscribers in different
// components share the same connection. AuthProvider owns the
// open/close lifecycle (open when a session exists, close on sign-out).
//
// We deliberately don't model match_found's old "owned by one hook"
// pattern from matchmake.ts: any number of subscribers can listen to
// any event type. useMatchmake just picks the events it cares about.
//
// Auth: the browser can't set Authorization on a WS upgrade, so the
// Supabase JWT travels as ?access_token=. The server's
// authorizationBearer reads it.

export type LobbyEventType =
  | "match_found"
  | "invite_received"
  | "invite_cancelled";

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

export type LobbyEvent =
  | { type: "match_found"; payload: MatchFoundPayload }
  | { type: "invite_received"; payload: InvitePayload }
  | { type: "invite_cancelled"; payload: InvitePayload };

type Listener = (ev: LobbyEvent) => void;

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 15000;

class UserSocket {
  private ws: WebSocket | null = null;
  private listeners = new Set<Listener>();
  private accessToken: string | null = null;
  private reconnectAttempt = 0;
  private reconnectTimer: number | null = null;
  // closed=true means the caller explicitly invoked close() — don't
  // attempt to reconnect. open() resets this.
  private closed = true;

  open(accessToken: string) {
    if (this.accessToken === accessToken && this.ws) return;
    // Token rotation: close the existing socket so the next reconnect
    // carries the new JWT in the query string.
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
      // Defensive copy: pass the same object to every listener so they
      // don't see mutations from each other.
      for (const fn of this.listeners) {
        try {
          fn(ev);
        } catch {
          /* listener error shouldn't crash the socket */
        }
      }
    };
    ws.onerror = () => {
      // Defer the failure surfacing to onclose — onerror is always
      // followed by onclose and we want a single transition.
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
      /* already closing */
    }
    this.ws = null;
  }

  private scheduleReconnect() {
    this.clearReconnectTimer();
    const delay = Math.min(
      RECONNECT_MAX_MS,
      RECONNECT_BASE_MS * Math.pow(2, this.reconnectAttempt),
    );
    this.reconnectAttempt += 1;
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
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
