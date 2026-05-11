import { supabase } from "./supabase";
import type {
  Game,
  JoinResponse,
  MoveResponse,
  Profile,
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
  createGame(players: number) {
    return request<Game>("/api/games", {
      method: "POST",
      body: JSON.stringify({ players }),
    });
  },

  getGame(id: string) {
    return request<Game>(`/api/games/${id}`);
  },

  joinGame(id: string, name: string, seat?: number) {
    return request<JoinResponse>(`/api/games/${id}/join`, {
      method: "POST",
      body: JSON.stringify(seat === undefined ? { name } : { name, seat }),
    });
  },

  getReplay(id: string) {
    return request<Replay>(`/api/games/${id}/replay`);
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
};

export { ApiError };
