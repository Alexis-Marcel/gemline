// Mirror of the Go server's wire types. Keep in sync with internal/server/dto.go
// and the constants in internal/game/types.go.

export type Color = -1 | 0 | 1 | 2 | 3 | 4 | 5 | 6;
export const OFF_BOARD: Color = -1;
export const EMPTY: Color = 0;

export type WinKind = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7;
export const WIN_NONE: WinKind = 0;
export const WIN_ALIGN6: WinKind = 1;
export const WIN_ALIGN5: WinKind = 2;
export const WIN_ALIGN4: WinKind = 3;
export const WIN_CAPTURE: WinKind = 4;
export const WIN_TIMEOUT: WinKind = 5;
export const WIN_RESIGN: WinKind = 6;
export const WIN_DRAW: WinKind = 7;

export type Status = "waiting" | "playing" | "finished";
export type Visibility = "public" | "private";

export interface Seat {
  index: number;
  color: Color;
  name: string;
  occupied: boolean;
  isBot: boolean;
}

export interface PlayerScore {
  color: Color;
  gemsRemaining: number;
  capturedPairs: number;
  timeRemainingMs: number;
}

export interface Thresholds {
  capturePairsWin: number;
  align4ToWin: number;
  align5ToWin: number;
  initialTimeMs: number;
  incrementMs: number;
}

export interface Game {
  id: string;
  status: Status;
  boardSide: number;
  cells: Color[];
  players: PlayerScore[];
  seats: Seat[];
  turn: number;
  winner: Color;
  winKind: WinKind;
  moveCount: number;
  thresholds: Thresholds;
  /** ISO-8601 timestamp when the active player's turn started. Empty when the game hasn't started yet. */
  turnStartedAt?: string;
  visibility: Visibility;
  /** ID of the rematch game spawned from this one, if any. */
  rematchGameId?: string;
  /** Seat index that currently has a draw offer pending, -1 when no offer is active. */
  drawOfferBy: number;
}

export interface RematchResponse {
  gameId: string;
  game: Game;
}

export interface Capture {
  victim: Color;
  capturer: Color;
  pair: [[number, number], [number, number]];
}

export interface MoveResponse {
  game: Game;
  captures: Capture[];
}

export interface JoinResponse {
  game: Game;
  seat: Seat;
  token: string;
}

/**
 * Each WsEvent that originates from the server-side EventPublisher carries
 * a per-game monotonic sequence number. Events that bypass persistence
 * (typically the initial state snapshot sent at WS open time, which is
 * tagged with the current event_seq rather than allocating a new one)
 * still include a seq so the client can detect catch-up gaps. Events
 * fetched through the HTTP /events?since=N catch-up endpoint also carry
 * a seq, which the client uses to dedup against live WS events that
 * arrive concurrently with the catch-up fetch.
 */
export type WsEvent =
  | { type: "state"; seq?: number; payload: Game }
  | { type: "move"; seq?: number; payload: MoveResponse }
  | { type: "chat"; seq?: number; payload: Message }
  | { type: "presence"; seq?: number; payload: { seatIndex: number; online: boolean } }
  | { type: "rated"; seq?: number; payload: GameRatings };

/** Row shape returned by GET /api/games/:id/events?since=N. */
export interface GameEventRow {
  seq: number;
  type: WsEvent["type"];
  payload: WsEvent["payload"];
}

/**
 * Per-seat rating snapshot. The applied-only fields (oldRating, newRating,
 * delta, result) are present when the game's Elo math has run, and absent
 * otherwise — the JSON tags use omitempty server-side.
 */
export interface SeatRating {
  seatIndex: number;
  userId: string;
  currentRating: number;
  oldRating?: number;
  newRating?: number;
  delta?: number;
  result?: "W" | "L" | "D";
}

/**
 * Returned by GET /api/games/:id/ratings and shipped as the payload of
 * the "rated" WS event. `rated` is the eligibility gate (public game,
 * no bots, no anon); `applied` becomes true once ApplyRatedGame has
 * run. Until then, seats only carry `currentRating`.
 */
export interface GameRatings {
  mode: "1v1" | "multi";
  rated: boolean;
  applied: boolean;
  seats: SeatRating[];
}

export interface Message {
  id: number;
  gameId: string;
  seatIndex: number;
  authorColor: Color;
  authorName: string;
  body: string;
  sentAt: string;
}

export interface Profile {
  userId: string;
  email: string;
  displayName: string;
}

export interface UserGame {
  gameId: string;
  status: Status;
  seatIndex: number;
  color: Color;
  winnerColor: Color;
  outcome: "won" | "lost" | "draw" | "ongoing";
  moveCount: number;
  createdAt: string;
  updatedAt: string;
}

export type RatingMode = "1v1" | "multi";

export interface UserStats {
  total: number;
  won: number;
  lost: number;
  ongoing: number;
  /** Current 1v1 Elo. 1200 is the default for a user who's never played a rated 1v1. */
  ratingOneVOne: number;
  /** Current multi Elo. 1200 is the default for a user who's never played a rated multi. */
  ratingMulti: number;
}

export interface LeaderboardEntry {
  userId: string;
  displayName: string;
  rating: number;
  games: number;
  wins: number;
  losses: number;
  draws: number;
}

export interface ReplayStep {
  ordinal: number;
  player: Color;
  q: number;
  r: number;
  captures: Capture[];
}

export interface Replay {
  gameId: string;
  boardSide: number;
  players: number;
  steps: ReplayStep[];
}
