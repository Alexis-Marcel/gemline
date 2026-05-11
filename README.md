# Gemline

Online multiplayer board game played on a hexagonal grid. Two parallel win conditions — align stones or capture opponent pairs.

## Rules

Each player has a colored stock of 50 gems. On your turn you place one gem on any free intersection of an 11-side hexagonal board (331 intersections in total). The placement may trigger captures and may end the game.

**Capture.** When the pattern `[you][opponent][opponent][you]` appears on any of the three line axes after your placement, with both opponent stones sharing the same color, those two stones are removed and credited to you as one captured pair. You can trigger several captures from a single placement, across different axes or the same axis. A placement that fills the middle of an opponent sandwich does *not* self-capture.

**Win.** A line of 6 of your color, anywhere on the board, is an instant win. Otherwise, the thresholds depend on the player count (configurable, see `internal/game/state.go`). In 2-player mode: three 4-alignments, two 5-alignments, or ten captured pairs.

## Status

The full vertical slice works end-to-end: a React frontend in `web/` lets two browsers join the same game and play in real time, the Go backend persists every move to Postgres so games survive a restart, and the engine itself is covered by tests (99.3% on `internal/game`).

**Works:** game creation, token-gated joining, turn enforcement, captures, alignments, win detection, WebSocket broadcast with auto-reconnect, persistence to Postgres with full state replay on load, two-player UI.

**Not yet:** AI opponents, rate limiting, multi-instance deployment (the WebSocket hub is per-process — a second backend instance wouldn't share broadcasts without pub/sub).

## Getting started

The stack is Go for the backend, Vite + React for the frontend, Postgres for persistence. Postgres runs via Docker Compose for local dev.

```sh
# 1. Start Postgres (once)
docker compose up -d

# 2. Backend
DATABASE_URL='postgres://gemline:gemline@localhost:5432/gemline?sslmode=disable' \
  go run ./cmd/server

# 3. Frontend (in another shell)
cd web && npm install && npm run dev
```

The frontend serves on `:5173` and proxies `/api` and `/ws` to the backend on `:8080`. Visit `http://localhost:5173` to play.

`DATABASE_URL` is optional: if unset, the backend falls back to a purely in-memory store (good for tests, but games are lost when the server stops).

### Quick smoke test against the API

```sh
GAME=$(curl -s -X POST localhost:8080/api/games \
  -H 'Content-Type: application/json' -d '{"players":2}' | jq -r .id)

TOK_A=$(curl -s -X POST localhost:8080/api/games/$GAME/join \
  -H 'Content-Type: application/json' -d '{"name":"Alice"}' | jq -r .token)
TOK_B=$(curl -s -X POST localhost:8080/api/games/$GAME/join \
  -H 'Content-Type: application/json' -d '{"name":"Bob"}' | jq -r .token)

curl -s -X POST localhost:8080/api/games/$GAME/moves \
  -H "Authorization: Bearer $TOK_A" \
  -H 'Content-Type: application/json' -d '{"q":0,"r":0}'
```

## API

| Method | Path                          | Body / auth                            | Purpose                       |
| ------ | ----------------------------- | -------------------------------------- | ----------------------------- |
| POST   | `/api/games`                  | `{"players": N}` (N ∈ 2..6)            | Create a game, returns its ID |
| POST   | `/api/games/{id}/join`        | `{"name": "..."}` (optional `"seat"`)  | Claim a seat, returns a token |
| POST   | `/api/games/{id}/moves`       | `{"q": Q, "r": R}` + `Bearer <token>`  | Play a stone                  |
| GET    | `/api/games/{id}`             |                                        | Snapshot of the game state    |
| GET    | `/ws/games/{id}`              | WebSocket                              | Live event stream             |
| GET    | `/healthz`, `/readyz`         |                                        | Health checks                 |

A game starts in `waiting` and transitions to `playing` once every seat is claimed. The WebSocket emits one `state` event on connect, then a `move` event after every placement. The server pings every 25s; the client reconnects with exponential backoff and ±30% jitter.

The bearer token returned by `join` is sent once and then only its SHA-256 is persisted — comparing on the hash means an attacker reading the DB cannot impersonate a player, and a player keeps their seat across server restarts.

### Coordinates

The board uses axial coordinates. The center is `(q=0, r=0)`. A position is on the board iff `|q| ≤ 10`, `|r| ≤ 10`, and `|q + r| ≤ 10`. The three line axes used for captures and alignments are `(1, 0)`, `(0, 1)`, and `(1, -1)`.

The `cells` field of the game DTO is a flat array of length `(2·side − 1)² = 441`, in row-major order over the axial bounding box. Values are `−1` for off-board slots, `0` for empty intersections, and `1..6` for player colors.

## Project layout

```
cmd/server/             backend entrypoint
internal/game/          pure engine — no I/O, no concurrency
internal/server/        HTTP + WebSocket layer, in-memory cache, Postgres repo
internal/db/            connection helper + embedded goose migrations
web/                    Vite + React + Tailwind frontend
  src/api/              wire types, REST client, WebSocket singleton
  src/components/       Board (SVG hex grid) and Scoreboard
  src/pages/            HomePage (create/join), GamePage (play)
docker-compose.yml      Postgres for local dev
```

Persistence is event-sourced: the database stores `games`, `seats`, and `moves`. On load, a game's state is rebuilt by replaying its moves through `ApplyMove`, so captures and win states are reproduced by the same rule engine used at play time. There is no separate snapshot; the move log is the source of truth.

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
