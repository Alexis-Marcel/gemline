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

export type WsEvent =
  | { type: "state"; payload: Game }
  | { type: "move"; payload: MoveResponse }
  | { type: "chat"; payload: Message }
  | { type: "presence"; payload: { seatIndex: number; online: boolean } };

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

export interface UserStats {
  total: number;
  won: number;
  lost: number;
  ongoing: number;
  /** Current Elo. 1200 is the default for a user who's never played a rated game. */
  rating: number;
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
