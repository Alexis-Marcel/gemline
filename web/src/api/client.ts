import { supabase } from "./supabase";
import type {
  Game,
  GameRatings,
  JoinResponse,
  LeaderboardEntry,
  Message,
  MoveResponse,
  Profile,
  ProfileSearchEntry,
  PublicProfile,
  Replay,
  UserGame,
  UserStats,
} from "./types";

class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

interface RequestOptions extends RequestInit {
  playerToken?: string; // seat-level token, sent in X-Player-Token
  skipAuth?: boolean;   // don't attach the Supabase JWT (e.g. health checks)
}

async function request<T>(path: string, init: RequestOptions = {}): Promise<T> {
  const { playerToken, skipAuth, ...rest } = init;
  const headers = new Headers(rest.headers);
  if (!headers.has("Content-Type") && rest.body) {
    headers.set("Content-Type", "application/json");
  }
  if (playerToken) {
    headers.set("X-Player-Token", playerToken);
  }
  if (!skipAuth) {
    const {
      data: { session },
    } = await supabase.auth.getSession();
    if (session?.access_token) {
      headers.set("Authorization", `Bearer ${session.access_token}`);
    }
  }

  const res = await fetch(path, { ...rest, headers });
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // not JSON
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  // Private-only (public games spawn via matchmake). `name` required for
  // anonymous callers; authed users may omit it (server uses profile name).
  createGame(players: number, name?: string) {
    const body: Record<string, unknown> = { players, visibility: "private" };
    if (name) body.name = name;
    return request<JoinResponse>("/api/games", {
      method: "POST",
      body: JSON.stringify(body),
    });
  },

  startGame(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/start`, {
      method: "POST",
      playerToken,
    });
  },

  // Returns immediately; the match arrives later as a lobby WS
  // "match_found" event. Idempotent while queued.
  enqueueMatchmake(players: number) {
    return request<{ queued: boolean; players: number; mode: string }>(
      "/api/matchmake/enqueue",
      {
        method: "POST",
        body: JSON.stringify({ players }),
      },
    );
  },

  // Idempotent — safe to call when not queued.
  cancelMatchmake() {
    return request<void>("/api/matchmake/enqueue", { method: "DELETE" });
  },

  // Reissues the authed caller's seat token for a game they were pre-seated
  // into (rematch). The reliable pull fallback when the lobby rematch_ready
  // push was missed; 403 if the caller owns no seat in the game.
  claimSeat(id: string) {
    return request<JoinResponse>(`/api/games/${id}/seat/claim`, {
      method: "POST",
    });
  },

  // Frees the caller's seat in a still-waiting game.
  leaveSeat(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/leave`, {
      method: "POST",
      playerToken,
    });
  },

  addBot(id: string, seatIndex: number) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/bot`, {
      method: "POST",
    });
  },

  // No token required (same auth model as addBot).
  removeBot(id: string, seatIndex: number) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/bot`, {
      method: "DELETE",
    });
  },

  // Reserves an empty seat for a named user (shown "en attente" until
  // they join).
  inviteSeat(id: string, seatIndex: number, userId: string, displayName: string) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/invite`, {
      method: "POST",
      body: JSON.stringify({ userId, displayName }),
    });
  },

  // Clears a pending invitation; the seat returns to empty.
  cancelSeatInvite(id: string, seatIndex: number) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/invite`, {
      method: "DELETE",
    });
  },

  // Invitee-side refusal (auth via JWT — no seat token yet). To accept,
  // the invitee calls joinGame, which routes them to the reserved seat.
  declineSeatInvite(id: string, seatIndex: number) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/invite/decline`, {
      method: "POST",
    });
  },

  // Propose-or-accept (server disambiguates). When every human seat has
  // accepted, the returned DTO carries rematchGameId — the redirect
  // signal. Idempotent: re-clicking by an accepted player is a no-op.
  offerRematch(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/rematch/offer`, {
      method: "POST",
      playerToken,
    });
  },

  // Clears any pending rematch offer (cancel or refuse — same outcome).
  declineRematch(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/rematch/decline`, {
      method: "POST",
      playerToken,
    });
  },

  resign(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/resign`, {
      method: "POST",
      playerToken,
    });
  },

  offerDraw(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/draw/offer`, {
      method: "POST",
      playerToken,
    });
  },

  acceptDraw(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/draw/accept`, {
      method: "POST",
      playerToken,
    });
  },

  declineDraw(id: string, playerToken: string) {
    return request<Game>(`/api/games/${id}/draw/decline`, {
      method: "POST",
      playerToken,
    });
  },

  getGame(id: string) {
    return request<Game>(`/api/games/${id}`);
  },

  // Returns rated:false for any game that isn't matchmaking-eligible
  // (private, or any seat is a bot/anon).
  getGameRatings(id: string) {
    return request<GameRatings>(`/api/games/${id}/ratings`, { skipAuth: true });
  },

  // Pass `name` only for anonymous users; authed users get it from profile.
  joinGame(id: string, name?: string, seat?: number) {
    const body: Record<string, unknown> = {};
    if (name) body.name = name;
    if (seat !== undefined) body.seat = seat;
    return request<JoinResponse>(`/api/games/${id}/join`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  },

  getReplay(id: string) {
    return request<Replay>(`/api/games/${id}/replay`);
  },

  getMessages(id: string) {
    return request<Message[]>(`/api/games/${id}/messages`);
  },

  postMessage(id: string, playerToken: string, body: string) {
    return request<Message>(`/api/games/${id}/messages`, {
      method: "POST",
      playerToken,
      body: JSON.stringify({ body }),
    });
  },

  postMove(id: string, playerToken: string, q: number, r: number) {
    return request<MoveResponse>(`/api/games/${id}/moves`, {
      method: "POST",
      playerToken,
      body: JSON.stringify({ q, r }),
    });
  },

  // Authenticated endpoints — these 401 if no JWT is attached.
  getMe() {
    return request<Profile>("/api/auth/me");
  },

  updateProfile(displayName: string) {
    return request<Profile>("/api/profile", {
      method: "PUT",
      body: JSON.stringify({ displayName }),
    });
  },

  getMyGames() {
    return request<UserGame[]>("/api/users/me/games");
  },

  getMyStats() {
    return request<UserStats>("/api/users/me/stats");
  },

  getLeaderboard(mode: "1v1" | "multi" = "1v1", limit = 50) {
    return request<LeaderboardEntry[]>(
      `/api/leaderboard?mode=${mode}&limit=${limit}`,
      { skipAuth: true },
    );
  },

  // 404 surfaces as ApiError so the page can render a "not found" state.
  getPublicProfile(userId: string) {
    return request<PublicProfile>(`/api/users/${encodeURIComponent(userId)}`, {
      skipAuth: true,
    });
  },

  // Backs the "Inviter un ami" modal. Auth-gated server-side.
  searchUsers(query: string, limit = 20) {
    const url = `/api/users/search?q=${encodeURIComponent(query)}&limit=${limit}`;
    return request<ProfileSearchEntry[]>(url);
  },
};

export { ApiError };
