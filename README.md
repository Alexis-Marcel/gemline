# Gemline

Real-time multiplayer board game on a hexagonal grid — 2 to 6 players, win by aligning stones or capturing opponent pairs.

**▶ Play it live: [gemline.werilo.fr](https://gemline.werilo.fr)** — anonymous play works, no account needed.

![Gemline game view](docs/media/game.png)

Built end to end as a solo project: a pure Go rules engine, an event-sourced backend that scales horizontally with single-writer game ownership, a React frontend, and a GitOps-managed Kubernetes deployment provisioned from scratch. The distributed-systems work is deliberate: the platform applies the patterns you'd reach for at much larger scale (leases with fencing, routed pub/sub, leader election), each introduced to fix a concrete correctness bug or scaling wall — see the [architecture](#architecture) section.

## Engineering highlights

- **Pure rules engine** (`internal/game`) — no I/O, no concurrency, 100 % statement coverage, enforced by a CI gate. All captures, alignments, clocks and win conditions live here and nowhere else.
- **Event-sourced persistence** — a game's state is rebuilt by replaying its move log through the same engine used at play time. No state snapshots; the journal is the source of truth.
- **Horizontal scaling via single-writer ownership** — each live game is owned by exactly one replica (Postgres leases, TTL + heartbeat), and every journal write is fenced by an ownership epoch so a zombie owner can *believe* it owns a game but can no longer *write*. Crashed owners are recovered by an orphan sweeper + replay.
- **Interest-routed fan-out** — live events travel over Redis pub/sub on per-game channels; a pod only subscribes to games its clients watch. The bus is lossy by design: events are sequenced, clients detect gaps and catch up from the journal (`/events?since=`).
- **Gateway / gamesvc split** — one binary, `ROLE=all|gateway|gamesvc`. Gateways terminate WebSockets and proxy commands to the owning gamesvc pod, so deploying game logic drops zero client connections.
- **Leader-elected matchmaker** — the durable queue stays in Postgres (`FOR UPDATE SKIP LOCKED`); a single elected leader runs matching, woken instantly by a Redis doorbell on enqueue.
- **Observability** — Prometheus metrics + alerts as code, Grafana dashboards, and OpenTelemetry traces that survive the cross-pod command forward (one trace: gateway → owner → SQL).
- **One-command infrastructure** — `make deploy`: Terraform (Hetzner VMs, LB, DNS) → Ansible (HA k3s, ArgoCD) → ArgoCD GitOps from this repo. Secrets via External Secrets + OIDC federation, zero long-lived cloud keys in the cluster. See [DEPLOY.md](DEPLOY.md).
- **Hardening** — seat tokens stored as SHA-256 only, per-caller token-bucket rate limiting, bounded request bodies (32 KiB), origin-locked CORS and WebSocket checks.

## The game

Each player has a stock of 50 gems. On your turn you place one gem on any free intersection of an 11-side hexagonal board (331 intersections). Two win tracks run in parallel:

- **Capture** — when `[you][opponent][opponent][you]` appears on any of the three line axes after your placement (both victims the same color), the pair is removed and credited to you. One placement can trigger several captures; filling the middle of an opponent sandwich does *not* self-capture.
- **Alignments** — a line of 6 is an instant win. Otherwise you win by accumulating maximal 4- or 5-alignments, or captured pairs, with thresholds per player count (`internal/game/state.go` is the source of truth):

| Players | 6-line | 5-lines | 4-lines | Captured pairs |
| ------- | ------ | ------- | ------- | -------------- |
| 2       | 1      | 3       | 8       | 12             |
| 3       | 1      | 3       | 6       | 10             |
| 4       | 1      | 2       | 5       | 9              |
| 5       | 1      | 2       | 4       | 7              |
| 6       | 1      | 2       | 4       | 6              |

Per-player chess clocks (10 min default), resign, draw offers, unanimous rematch, in-game chat, heuristic bots to fill seats, Elo rating with a global leaderboard, and full game replay round out the product.

## Architecture

```mermaid
flowchart LR
  C["Browsers"] -- "WebSocket / REST" --> GW["gateway ×N<br/>WS termination · auth · presence"]
  GW -- "forward command" --> GS["gamesvc ×N<br/>1 live game = 1 owner<br/>clocks · bots · matchmaker leader"]
  GS -- "PUBLISH game:{id}" --> R[("Redis<br/>per-game pub/sub · doorbell")]
  R -- "SUBSCRIBE game:{id}" --> GW
  GS -- "append (seq, epoch)" --> PG[("Postgres<br/>event journal · leases · accounts")]
  GW -. "JWT verify (JWKS)" .-> SB["Supabase Auth"]
```

**Life of a move:**

1. The player sends a move over WebSocket/REST to any gateway.
2. The gateway resolves the game's lease and forwards the command to the owning gamesvc pod (fallback chain: owner → gamesvc pool → handle locally — affinity is an optimization, never a reason to fail a command).
3. The owner validates in memory, increments `seq`, appends to the journal stamped with its lease epoch (a stale epoch is rejected atomically in SQL), and re-arms the clock.
4. It publishes the event on `game:{id}`; only gateways with clients on that game receive it and push it down their WebSockets — including to the move's author, confirmed by the same path as everyone else.
5. If a client detects a `seq` gap (missed event, reconnect), it catches up with `GET /events?since=N`.

The invariant hierarchy: **the transport is lossy, the journal is the truth, clients reconcile by offset.** That contract predates every infrastructure change — it's what allowed swapping the whole transport (Postgres LISTEN/NOTIFY → Redis) and splitting the backend into roles without ever touching the client protocol.

Everything degrades gracefully: without `DATABASE_URL` games live in memory and anonymous play still works; without Redis the binary runs single-process; without Supabase only authenticated endpoints return 401.

## Getting started

The stack is Go (backend), Vite + React + Tailwind (frontend), Postgres (persistence), Redis (cross-replica fan-out), Supabase (auth). Postgres can be your Supabase project's database or the local Docker one for offline dev.

### One-time setup

1. Create a free project at [supabase.com](https://supabase.com).
2. Grab three values from your project settings:
   - **DATABASE_URL** — Settings → Database → Connection string (URI format).
   - **VITE_SUPABASE_URL** and **VITE_SUPABASE_PUBLISHABLE_KEY** — Settings → API.
   - **SUPABASE_URL** — Settings → API → Project URL (the backend fetches its JWKS from it).
3. Copy `web/.env.example` to `web/.env.local` and paste the `VITE_SUPABASE_*` values.

### Run

```sh
# Redis (and a local Postgres if you're not using Supabase)
docker compose up -d

# Backend
DATABASE_URL='<your Supabase DATABASE_URL>' \
SUPABASE_URL='https://<your-project>.supabase.co' \
REDIS_URL='redis://localhost:6379' \
  go run ./cmd/server

# Frontend (separate shell)
cd web && npm install && npm run dev
```

The frontend serves on `:5173` and proxies `/api` and `/ws` to the backend on `:8080`. Visit `http://localhost:5173` to play.

`DATABASE_URL` and the auth variables are optional — unset, games stay in memory and anonymous play still works. `REDIS_URL` is required whenever `DATABASE_URL` is set. `ALLOWED_ORIGINS` (comma-separated) locks down CORS and the WebSocket origin check in production; leave it empty in dev. The backend also reads `.env` / `.env.local` at the repo root, so you can `cp .env.example .env` once and forget about it.

<details>
<summary><strong>Quick smoke test against the API</strong></summary>

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

</details>

## API

Two parallel auth tokens travel on requests:

- **`Authorization: Bearer <JWT>`** — Supabase-issued user JWT. Identifies *who* the client is. Optional on game endpoints (anonymous play is allowed), required on `/api/auth/*`, `/api/profile`, `/api/users/me/*`.
- **`X-Player-Token: <seat-token>`** — per-seat secret returned once by `/join`. Identifies *which seat* in *which game* the client controls. Only its SHA-256 is persisted: an attacker reading the DB cannot impersonate a player, and a player keeps their seat across restarts.

<details>
<summary><strong>Full endpoint reference</strong></summary>

**Games & seats**

| Method | Path                                         | Auth          | Purpose                                       |
| ------ | -------------------------------------------- | ------------- | --------------------------------------------- |
| POST   | `/api/games`                                 | optional JWT  | Create a private game (auto-joins the caller) |
| GET    | `/api/games/{id}`                            |               | Snapshot of the game state                    |
| POST   | `/api/games/{id}/join`                       | optional JWT  | Claim a seat (linked to user if signed in)    |
| POST   | `/api/games/{id}/leave`                      | seat token    | Vacate a seat in a waiting game               |
| POST   | `/api/games/{id}/seat/resolve`               | JWT required  | Pull the seat token for a seat you were pre-seated into |
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
| GET    | `/api/matchmake/current`    | JWT required | Poll the game you were matched into (navigation fallback) |

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

**Coordinates.** The board uses axial coordinates; the center is `(q=0, r=0)`. A position is on the board iff `|q| ≤ 10`, `|r| ≤ 10`, and `|q + r| ≤ 10`. The three line axes for captures and alignments are `(1, 0)`, `(0, 1)`, and `(1, -1)`. The `cells` field of the game DTO is a flat array of length `(2·side − 1)² = 441` in row-major order over the axial bounding box: `−1` off-board, `0` empty, `1..6` player colors.

</details>

## Project layout

```
cmd/server/             backend entrypoint — one binary, ROLE=all|gateway|gamesvc
internal/game/          pure rules engine — no I/O, no concurrency
internal/server/        HTTP + WebSocket layer, in-memory cache, Postgres repo,
                        matchmaking, chat, clocks, presence, bots, JWT middleware,
                        cross-pod command forwarding
internal/lease/         per-game ownership leases (single writer + fencing) and
                        leader election
internal/bus/           Redis pub/sub with per-game channel routing
internal/ai/            heuristic bot move selection
internal/elo/           Elo rating math
internal/db/            connection pool + embedded goose migrations
internal/tracing/       OpenTelemetry setup
web/                    Vite + React + Tailwind frontend
  src/api/              wire types, REST client, WebSocket singletons, Supabase client
  src/auth/             AuthProvider + useAuth hook
  src/notifications/    cross-page invitation toasts
  src/components/       Board, Scoreboard, chat, clocks, rematch, replay, …
  src/pages/            Home, Game, Login, Profile, PublicProfile, Leaderboard, Matchmaking
docker-compose.yml      offline-friendly Postgres + Redis for local dev
deploy/                 Terraform + Ansible + ArgoCD + k8s — see DEPLOY.md
```

Persistence is event-sourced: a game's state is rebuilt by replaying its `moves` through `ApplyMove`, so captures and win states are reproduced by the same rule engine used at play time. The move log is the source of truth — there is no separate state snapshot. The schema (`internal/db/migrations/`, applied via goose on startup) also holds seats, profiles, ratings + history, chat messages, the matchmaking queue, the sequenced `game_events` journal, and the game/leader lease tables.

## Tests

```sh
go test ./...                                   # engine + REST + WS + bus (hermetic)
go test -cover ./internal/game/                 # 100.0% of statements, gated ≥95% in CI

# Integration tests against a real Postgres (started via docker compose):
GEMLINE_TEST_DATABASE_URL='postgres://gemline:gemline@localhost:5432/gemline?sslmode=disable' \
  go test ./internal/db/... ./internal/server/... ./internal/lease/...
```

The coverage number is honest about its scope: it is statement coverage of the rules engine — the package where a silent bug corrupts a game without crashing anything. It is high because the engine is a pure package, and it stays high because CI fails below 95 %.

- The **hermetic suite** covers hex bounds, capture detection on all three axes, capture chaining, suicide non-capture, alignment counting before/after captures, every win kind, clocks (accrual, increment, flag, skew tolerance), resign/draw/forfeit endings for 2 and 3+ players, turn rotation, and every documented error path — plus the REST round trip, WebSocket broadcast, and the Redis bus against miniredis.
- The **integration suite** verifies that a game replayed from Postgres reproduces the exact in-memory state of the original — including captures re-executed by the engine during replay — and exercises leases, fencing, and takeover against a real database.
- [SMOKE_TESTS.md](SMOKE_TESTS.md) scripts the end-to-end browser scenarios (multi-account invitations, rematch, cross-page notifications) that unit tests can't reach.

## Deployment

The production instance runs on a self-hosted HA k3s cluster on Hetzner Cloud, stood up with one command (`make deploy`) and managed by GitOps from this repo:

| Layer | Tool |
|---|---|
| Infrastructure | Terraform — VMs, private network, LB, firewall, Cloudflare DNS; remote state on S3 + DynamoDB |
| Configuration | Ansible — HA k3s (embedded etcd), cert-manager, ArgoCD app-of-apps |
| GitOps | ArgoCD — auto-sync + self-heal from `main`; CI builds images to GHCR and commits the version bumps |
| Secrets | AWS Secrets Manager + External Secrets Operator, OIDC-federated — zero long-lived cloud keys in the cluster |
| TLS | cert-manager + Let's Encrypt via Traefik |
| Observability | kube-prometheus-stack + Tempo — metrics, dashboards, alerts as code, distributed traces |

Full walkthrough in [DEPLOY.md](DEPLOY.md).
