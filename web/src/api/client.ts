import { supabase } from "./supabase";
import type {
  Game,
  JoinResponse,
  LeaderboardEntry,
  Message,
  MoveResponse,
  Profile,
  RematchResponse,
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
      /* not JSON */
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  // createGame creates a private game and auto-joins the caller. Public
  // games are spawned implicitly by matchmake() — this endpoint is now
  // private-only on the server. `name` is required for anonymous callers;
  // authenticated users may leave it undefined (server uses profile name).
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

  // matchmake requires a signed-in user — the server 401s otherwise.
  // Atomic: the caller is auto-joined into the matched game, so the client
  // gets back the seat token and can navigate straight in without a
  // follow-up /join call.
  matchmake(players: number) {
    return request<JoinResponse>("/api/games/matchmake", {
      method: "POST",
      body: JSON.stringify({ players }),
    });
  },

  // leaveSeat frees the caller's seat in a still-waiting game (cancel
  // matchmaking, or back out of a private invite before play starts).
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

  rematch(id: string) {
    return request<RematchResponse>(`/api/games/${id}/rematch`, {
      method: "POST",
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

  // joinGame: pass `name` only for anonymous users — authenticated users
  // let the server fill it from their profile display name.
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
};

export { ApiError };
