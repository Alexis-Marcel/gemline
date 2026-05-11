// Mirror of the Go server's wire types. Keep in sync with internal/server/dto.go
// and the constants in internal/game/types.go.

export type Color = -1 | 0 | 1 | 2 | 3 | 4 | 5 | 6;
export const OFF_BOARD: Color = -1;
export const EMPTY: Color = 0;

export type WinKind = 0 | 1 | 2 | 3 | 4;
export const WIN_NONE: WinKind = 0;
export const WIN_ALIGN6: WinKind = 1;
export const WIN_ALIGN5: WinKind = 2;
export const WIN_ALIGN4: WinKind = 3;
export const WIN_CAPTURE: WinKind = 4;

export type Status = "waiting" | "playing" | "finished";

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
}

export interface Thresholds {
  capturePairsWin: number;
  align4ToWin: number;
  align5ToWin: number;
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
  | { type: "chat"; payload: Message };

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
