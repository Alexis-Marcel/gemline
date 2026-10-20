# Gemline — web frontend

Single-page app for Gemline: the board, lobby, matchmaking, profiles, and
the live game experience. **Vite + React + TypeScript + Tailwind**, with
accounts from an OIDC provider (the Compte Eriphe / Authentik, PKCE).

See the root [`README.md`](../README.md) for the game rules and backend,
and [`DEPLOY.md`](../DEPLOY.md) for how this is shipped (built into a
static bundle served by Caddy behind Traefik).

## Run

```sh
cp .env.example .env.local      # OIDC issuer + client ID
npm install
npm run dev                     # http://localhost:5173
```

The dev server proxies `/api`, `/ws`, `/healthz`, and `/readyz` to the
backend on `http://localhost:8080` (see `vite.config.ts`), so run the Go
server alongside it.

### Configuration

None at build time: the frontend reads the host's account method (none,
oidc, proxy, local — see the root `.env.example`) from the backend's
`/api/config` when it loads, so the same image serves every deployment.

### Scripts

```sh
npm run dev       # dev server with HMR
npm run build     # type-check + production bundle to dist/
npm run preview   # serve the built bundle locally
npm run lint      # eslint
```

## Layout

```
src/
  api/            wire types, REST client, WebSocket singletons, OIDC client
  auth/           AuthProvider + useAuth hook (OIDC session, /auth/callback)
  notifications/  cross-page invitation toasts (per-user lobby socket)
  hooks/          in-play actions, rematch flow
  lib/            hex math, replay, colors, haptics, sounds, snapshots
  components/     Board, Scoreboard, chat, clocks, rematch, replay, modals, …
  pages/          Home, Game, Login, Profile, PublicProfile, Leaderboard, Matchmaking
```

### Real-time

Two WebSocket connections back the live experience:

- **Game socket** (`/ws/games/{id}`) — one per open game; pushes `state`,
  `move`, `presence`, and rating events. Auto-reconnects with backoff and
  catches up missed events via `GET /api/games/{id}/events?since=`.
- **Lobby socket** (`/ws/lobby`) — one per signed-in user; delivers
  invitations, `match_found`, and rematch hand-offs even when you're not
  on a game page. Survives access-token renewal by reconnecting with
  the new `access_token`.
