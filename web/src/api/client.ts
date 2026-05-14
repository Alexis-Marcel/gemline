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
  //
  // Legacy synchronous path — superseded by enqueueMatchmake +
  // lobbySocket. Kept here because the hermetic server tests still
  // call it.
  matchmake(players: number) {
    return request<JoinResponse>("/api/games/matchmake", {
      method: "POST",
      body: JSON.stringify({ players }),
    });
  },

  // enqueueMatchmake puts the caller in the matchmaking queue and
  // returns immediately (HTTP 202). The actual match comes through
  // the lobby WebSocket as a "match_found" event. Idempotent —
  // calling it again while still queued just refreshes the position.
  enqueueMatchmake(players: number) {
    return request<{ queued: boolean; players: number; mode: string }>(
      "/api/matchmake/enqueue",
      {
        method: "POST",
        body: JSON.stringify({ players }),
      },
    );
  },

  // cancelMatchmake removes the caller's ticket. Always 204 — safe to
  // call when not queued (the lobby WS close handler also invokes it
  // as a safety net so a closed tab clears the row).
  cancelMatchmake() {
    return request<void>("/api/matchmake/enqueue", { method: "DELETE" });
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

  // removeBot vacates a bot-occupied seat in a private waiting game.
  // Server-side guards: status=waiting + visibility=private + seat must
  // actually be a bot. Same auth model as addBot — no token required.
  removeBot(id: string, seatIndex: number) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/bot`, {
      method: "DELETE",
    });
  },

  // inviteSeat reserves an empty seat for a named user. The seat
  // shows their name with an "en attente" affordance until they
  // navigate to the game URL and join. Server-side guards:
  // private + waiting + seat empty.
  inviteSeat(id: string, seatIndex: number, userId: string, displayName: string) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/invite`, {
      method: "POST",
      body: JSON.stringify({ userId, displayName }),
    });
  },

  // cancelSeatInvite clears a pending invitation on a seat. The
  // seat returns to the empty state. Server-side: must actually be
  // an invited seat (not a human, not a bot, not empty).
  cancelSeatInvite(id: string, seatIndex: number) {
    return request<Game>(`/api/games/${id}/seats/${seatIndex}/invite`, {
      method: "DELETE",
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

  // getGameRatings drives the in-game Elo line and the end-of-game
  // modal's delta section. Returns rated:false for any game that
  // isn't matchmaking-eligible (private, or any seat is a bot/anon).
  getGameRatings(id: string) {
    return request<GameRatings>(`/api/games/${id}/ratings`, { skipAuth: true });
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

  // getPublicProfile fetches the read-only profile of any user.
  // 404 surfaces as ApiError so the page can render a "user not
  // found" state cleanly.
  getPublicProfile(userId: string) {
    return request<PublicProfile>(`/api/users/${encodeURIComponent(userId)}`, {
      skipAuth: true,
    });
  },

  // searchUsers backs the "Inviter un ami" modal. Auth-gated
  // server-side — the modal only renders for signed-in users anyway.
  searchUsers(query: string, limit = 20) {
    const url = `/api/users/search?q=${encodeURIComponent(query)}&limit=${limit}`;
    return request<ProfileSearchEntry[]>(url);
  },
};

export { ApiError };
