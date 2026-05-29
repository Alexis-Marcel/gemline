# Gemline

Online multiplayer board game played on a hexagonal grid. Two parallel win conditions — align stones or capture opponent pairs.

## Rules

Each player has a colored stock of 50 gems. On your turn you place one gem on any free intersection of an 11-side hexagonal board (331 intersections in total). The placement may trigger captures and may end the game.

**Capture.** When the pattern `[you][opponent][opponent][you]` appears on any of the three line axes after your placement, with both opponent stones sharing the same color, those two stones are removed and credited to you as one captured pair. You can trigger several captures from a single placement, across different axes or the same axis. A placement that fills the middle of an opponent sandwich does *not* self-capture.

**Win.** A line of 6 of your color, anywhere on the board, is an instant win. Otherwise, the thresholds depend on the player count (see the table below, and `internal/game/state.go` for the source of truth). In 2-player mode: eight 4-alignments, three 5-alignments, or twelve captured pairs (24 gems gobbled two-by-two).

| Players | 6-line | 5-lines | 4-lines | Captured pairs |
| ------- | ------ | ------- | ------- | -------------- |
| 2       | 1      | 3       | 8       | 12             |
| 3       | 1      | 3       | 6       | 10             |
| 4       | 1      | 2       | 5       | 9              |
| 5       | 1      | 2       | 4       | 7              |
| 6       | 1      | 2       | 4       | 6              |

## Status

The full vertical slice works end-to-end: a React frontend in `web/` lets browsers join the same game and play in real time, the Go backend persists every move to Postgres so games survive a restart, signed-in users get profiles + history + Elo, and the engine itself is covered by tests (99.3% on `internal/game`). The backend is horizontally scalable — multiple replicas share live events through a Postgres `LISTEN/NOTIFY` backplane.

**Works:**

- **Play** — game creation, token-gated joining (anonymous *or* authenticated), turn enforcement, captures, alignments, win detection for 2–6 players.
- **Real time** — WebSocket broadcast with auto-reconnect and event catch-up (`/events?since=`), live presence badges, per-player clocks, resign and draw offers.
- **Matchmaking** — async queue for 1v1 and 3–6-player games with live count + ETA, Elo rating, and a global leaderboard.
- **Social** — cross-page game invitations (with toast + notification chime), unanimous rematch, in-game chat.
- **Bots** — heuristic AI opponents (`internal/ai`) that can fill seats in private games.
- **Accounts** — Supabase email/password auth, public profiles, per-user history and aggregate stats.
- **Persistence & scale** — every move stored in Postgres with full state replay on load; multi-replica backend via the Postgres backplane (cross-pod event fan-out + cache invalidation).

**Not yet:** rate limiting.

## Getting started

The stack is Go for the backend, Vite + React for the frontend, Postgres for persistence, and Supabase for user authentication. Postgres can be your Supabase project's database (recommended) or a local Docker Postgres for offline dev.

### One-time setup

1. Create a free project at [supabase.com](https://supabase.com).
2. Grab three values from your project settings:
   - **DATABASE_URL** — Settings → Database → Connection string (URI format).
   - **VITE_SUPABASE_URL** and **VITE_SUPABASE_PUBLISHABLE_KEY** — Settings → API.
   - **SUPABASE_URL** — Settings → API → Project URL. The backend fetches the JWKS that verifies user JWTs.
3. Frontend env vars: copy `web/.env.example` to `web/.env.local` and paste the `VITE_SUPABASE_*` values.

### Run

```sh
# Backend
DATABASE_URL='<your Supabase DATABASE_URL>' \
SUPABASE_URL='https://<your-project>.supabase.co' \
  go run ./cmd/server

# Frontend (separate shell)
cd web && npm install && npm run dev
```

The frontend serves on `:5173` and proxies `/api` and `/ws` to the backend on `:8080`. Visit `http://localhost:5173` to play.

Both `DATABASE_URL` and the auth variables are optional — if unset, games stay in memory and `/api/auth/*` returns 401, but anonymous play still works. A third optional variable, `ALLOWED_ORIGINS` (comma-separated), locks down CORS and the WebSocket origin check; leave it empty in dev (permissive `*`), set it to your frontend origin(s) in production. The backend reads its environment from a `.env` (and optional `.env.local` override) at the repo root in addition to the shell, so you can `cp .env.example .env` once and forget about it. For an offline-friendly dev loop without Supabase, `docker compose up -d` brings up a local Postgres on `localhost:5432` with credentials `gemline / gemline`.

### Quick smoke test against the API

```sh
# Create a private 2-player game. The creator is auto-seated and gets a
# seat token back, so the response is a {game, seat, token} envelope.
CREATE=$(curl -s -X POST localhost:8080/api/games \
  -H 'Content-Type: application/json' -d '{"players":2,"name":"Alice"}')
GAME=$(echo "$CREATE" | jq -r .game.id)
TOK_A=$(echo "$CREATE" | jq -r .token)

# Bob claims the remaining seat — the game flips to "playing".
TOK_B=$(curl -s -X POST localhost:8080/api/games/$GAME/join \
  -H 'Content-Type: application/json' -d '{"name":"Bob"}' | jq -r .token)

# Alice plays the center intersection. The seat token goes in X-Player-Token.
curl -s -X POST localhost:8080/api/games/$GAME/moves \
  -H "X-Player-Token: $TOK_A" \
  -H 'Content-Type: application/json' -d '{"q":0,"r":0}'
```

## API

Two parallel auth tokens travel on requests:

- **`Authorization: Bearer <JWT>`** — Supabase-issued user JWT. Identifies *who* the client is. Optional on game endpoints (anonymous play is allowed) and required on `/api/auth/*`, `/api/profile`, `/api/users/me/*`.
- **`X-Player-Token: <seat-token>`** — per-seat secret returned once by `/join`. Identifies *which seat* in *which game* the client controls. Required on `/api/games/{id}/moves`.

**Games & seats**

| Method | Path                                         | Auth          | Purpose                                       |
| ------ | -------------------------------------------- | ------------- | --------------------------------------------- |
| POST   | `/api/games`                                 | optional JWT  | Create a private game (auto-joins the caller) |
| GET    | `/api/games/{id}`                            |               | Snapshot of the game state                    |
| POST   | `/api/games/{id}/join`                       | optional JWT  | Claim a seat (linked to user if signed in)    |
| POST   | `/api/games/{id}/leave`                      | seat token    | Vacate a seat in a waiting game               |
| POST   | `/api/games/{id}/start`                      | seat token    | Start a private game (fills empties with bots)|
| POST   | `/api/games/{id}/moves`                      | seat token    | Play a stone                                  |
| POST   | `/api/games/{id}/resign`                     | seat token    | Resign                                        |
| POST   | `/api/games/{id}/draw/{offer,accept,decline}`| seat token    | Draw offer lifecycle                          |
| POST   | `/api/games/{id}/rematch/{offer,decline}`    | seat token    | Unanimous rematch lifecycle                   |
| POST   | `/api/games/{id}/seats/{idx}/bot`            |               | Add a bot to a seat (private waiting game)    |
| DELETE | `/api/games/{id}/seats/{idx}/bot`            |               | Remove a bot from a seat                      |
| POST   | `/api/games/{id}/seats/{idx}/invite`         | optional JWT  | Reserve a seat for a named user               |
| DELETE | `/api/games/{id}/seats/{idx}/invite`         |               | Cancel a pending seat invitation              |
| POST   | `/api/games/{id}/seats/{idx}/invite/decline` | JWT required  | Invitee declines an invitation                |

**Real-time & history**

| Method | Path                          | Auth | Purpose                                            |
| ------ | ----------------------------- | ---- | -------------------------------------------------- |
| GET    | `/ws/games/{id}`              |      | Live game event stream                             |
| GET    | `/ws/lobby`                   | JWT  | Per-user lobby stream (invites, match found, rematch) |
| GET    | `/api/games/{id}/events?since=`|     | Event catch-up after reconnect                     |
| GET    | `/api/games/{id}/replay`      |      | Ordered move log for replay                        |
| GET    | `/api/games/{id}/ratings`     |      | Elo snapshot for the game                          |
| GET    | `/api/games/{id}/messages`    |      | Chat history                                       |
| POST   | `/api/games/{id}/messages`    | seat token | Post a chat message                          |

**Matchmaking**

| Method | Path                        | Auth         | Purpose                                  |
| ------ | --------------------------- | ------------ | ---------------------------------------- |
| POST   | `/api/matchmake/enqueue`    | JWT required | Join the matchmaking queue (1v1 or 3–6)  |
| DELETE | `/api/matchmake/enqueue`    | JWT required | Leave the queue                          |

**Users, profiles & ops**

| Method | Path                    | Auth         | Purpose                          |
| ------ | ----------------------- | ------------ | -------------------------------- |
| GET    | `/api/auth/me`          | JWT required | Authenticated user's profile     |
| PUT    | `/api/profile`          | JWT required | Update display name              |
| GET    | `/api/users/me/games`   | JWT required | Caller's game history            |
| GET    | `/api/users/me/stats`   | JWT required | Caller's aggregate stats         |
| GET    | `/api/users/search?q=`  | JWT required | Search profiles (for invites)    |
| GET    | `/api/users/{userId}`   |              | Public profile                   |
| GET    | `/api/leaderboard`      |              | Global Elo leaderboard           |
| GET    | `/healthz`, `/readyz`   |              | Health / readiness checks        |
| GET    | `/metrics`              |              | Prometheus metrics               |

A game starts in `waiting` and transitions to `playing` once every seat is claimed. The WebSocket emits one `state` event on connect, then a `move` event after every placement. The server pings every 25s; the client reconnects with exponential backoff and ±30% jitter.

The seat token returned by `join` is sent once and then only its SHA-256 is persisted — comparing on the hash means an attacker reading the DB cannot impersonate a player, and a player keeps their seat across server restarts.

### Coordinates

The board uses axial coordinates. The center is `(q=0, r=0)`. A position is on the board iff `|q| ≤ 10`, `|r| ≤ 10`, and `|q + r| ≤ 10`. The three line axes used for captures and alignments are `(1, 0)`, `(0, 1)`, and `(1, -1)`.

The `cells` field of the game DTO is a flat array of length `(2·side − 1)² = 441`, in row-major order over the axial bounding box. Values are `−1` for off-board slots, `0` for empty intersections, and `1..6` for player colors.

## Project layout

```
cmd/server/             backend entrypoint
internal/game/          pure engine — no I/O, no concurrency
internal/server/        HTTP + WebSocket layer, in-memory cache, Postgres repo,
                        matchmaking, chat, clocks, presence, bots, JWT middleware
internal/ai/            heuristic bot move selection
internal/elo/           Elo rating math
internal/backplane/     Postgres LISTEN/NOTIFY pub/sub for cross-pod fan-out
internal/db/            connection pool + embedded goose migrations
web/                    Vite + React + Tailwind frontend
  src/api/              wire types, REST client, WebSocket singletons, Supabase client
  src/auth/             AuthProvider + useAuth hook
  src/notifications/    cross-page invitation toasts
  src/components/       Board, Scoreboard, chat, clocks, rematch, replay, …
  src/pages/            Home, Game, Login, Profile, PublicProfile, Leaderboard, Matchmaking
docker-compose.yml      offline-friendly Postgres for local dev
deploy/                 Terraform + Ansible + ArgoCD + k8s — see DEPLOY.md
```

Persistence is event-sourced: a game's state is rebuilt by replaying its `moves` through `ApplyMove`, so captures and win states are reproduced by the same rule engine used at play time. The move log is the source of truth — there is no separate state snapshot. The schema (in `internal/db/migrations/`, applied automatically via goose on startup) also holds `seats`, `profiles`, `ratings` + `rating_history`, `messages` (chat), `matchmake_queue`, and `game_events` (the sequenced WebSocket event log used for reconnect catch-up).

## Tests

```sh
go test ./...                                   # engine + REST + WS (hermetic)
go test -cover ./internal/game/...              # 99.3 %

# Integration tests that hit a real Postgres (started via docker compose):
GEMLINE_TEST_DATABASE_URL='postgres://gemline:gemline@localhost:5432/gemline?sslmode=disable' \
  go test ./internal/db/... ./internal/server/...
```

The hermetic suite covers hex bounds, capture detection on all three axes, capture chaining (multi-axis and multi-pattern on a single axis), suicide non-capture, alignment counting before and after captures, win conditions for each kind (4, 5, 6 alignments, capture threshold), turn rotation for 2- and 3-player games, and every documented error path. The server tests cover the REST round trip and the WebSocket broadcast.

The integration suite verifies that a game replayed from Postgres produces the same in-memory state as the original — including the capture scenario, where two stones are removed by the rule engine during replay.
