import type { Game, JoinResponse, MoveResponse } from "./types";

class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(
  path: string,
  init: RequestInit & { token?: string } = {},
): Promise<T> {
  const { token, ...rest } = init;
  const headers = new Headers(rest.headers);
  if (!headers.has("Content-Type") && rest.body) {
    headers.set("Content-Type", "application/json");
  }
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  const res = await fetch(path, { ...rest, headers });
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      /* response was not JSON */
    }
    throw new ApiError(res.status, message);
  }
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

  postMove(id: string, token: string, q: number, r: number) {
    return request<MoveResponse>(`/api/games/${id}/moves`, {
      method: "POST",
      token,
      body: JSON.stringify({ q, r }),
    });
  },
};

export { ApiError };
