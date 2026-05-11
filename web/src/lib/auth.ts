// Per-game player credentials, persisted so a page refresh doesn't lose your seat.

interface Credentials {
  token: string;
  seatIndex: number;
  name: string;
}

const KEY = (gameId: string) => `gemline:auth:${gameId}`;

export function saveCredentials(gameId: string, creds: Credentials): void {
  localStorage.setItem(KEY(gameId), JSON.stringify(creds));
}

export function loadCredentials(gameId: string): Credentials | null {
  const raw = localStorage.getItem(KEY(gameId));
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Credentials;
  } catch {
    return null;
  }
}

export function clearCredentials(gameId: string): void {
  localStorage.removeItem(KEY(gameId));
}
