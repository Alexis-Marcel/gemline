# Gemline — web frontend

Single-page app for Gemline: the board, lobby, matchmaking, profiles, and
the live game experience. **Vite + React + TypeScript + Tailwind**, with
Supabase for auth.

See the root [`README.md`](../README.md) for the game rules and backend,
and [`DEPLOY.md`](../DEPLOY.md) for how this is shipped (built into a
static bundle served by Caddy behind Traefik).

## Run

```sh
cp .env.example .env.local      # fill in your Supabase project values
npm install
npm run dev                     # http://localhost:5173
```

The dev server proxies `/api`, `/ws`, `/healthz`, and `/readyz` to the
backend on `http://localhost:8080` (see `vite.config.ts`), so run the Go
server alongside it.

### Environment

| Variable | Purpose |
|---|---|
| `VITE_SUPABASE_URL` | Supabase project URL (Settings → API). |
| `VITE_SUPABASE_PUBLISHABLE_KEY` | Publishable key (`sb_publishable_...`). Safe to ship — auth is enforced server-side via the JWT. |

Both are baked in at **build time** (Vite inlines `VITE_*` vars), so the
production image is built with them as Docker build args — see
`web/Dockerfile` and the `build-web` job in `.github/workflows/deploy.yml`.

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
  api/            wire types, REST client, WebSocket singletons, Supabase client
  auth/           AuthProvider + useAuth hook (Supabase session)
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
  on a game page. Survives Supabase token refresh by reconnecting with
  the new `access_token`.
