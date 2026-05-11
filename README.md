# Gemline

Online multiplayer board game played on a hexagonal grid. Two parallel win conditions — align stones or capture opponent pairs.

## Rules

Each player has a colored stock of 50 gems. On your turn you place one gem on any free intersection of an 11-side hexagonal board (331 intersections in total). The placement may trigger captures and may end the game.

**Capture.** When the pattern `[you][opponent][opponent][you]` appears on any of the three line axes after your placement, with both opponent stones sharing the same color, those two stones are removed and credited to you as one captured pair. You can trigger several captures from a single placement, across different axes or the same axis. A placement that fills the middle of an opponent sandwich does *not* self-capture.

**Win.** A line of 6 of your color, anywhere on the board, is an instant win. Otherwise, the thresholds depend on the player count (configurable, see `internal/game/state.go`). In 2-player mode: three 4-alignments, two 5-alignments, or ten captured pairs.

## Status

The server-side game engine and the multiplayer plumbing are complete and covered by tests (99.3% on `internal/game`). There is no frontend yet — you can drive the API with `curl` or the test suite, but you can't yet click on a board.

**Works:** game creation, joining with token-based seat assignment, turn enforcement, captures, alignments, win detection, WebSocket broadcast of moves to all subscribed clients.

**Not yet:** UI, persistence (state is in-memory and lost on restart), reconnection / disconnect handling, AI opponents, rate limiting.

## Getting started

```sh
go test ./...           # 28 engine tests + 8 server tests
go run ./cmd/server     # listens on :8080 (override with ADDR=:9000)
```

Quick smoke test against a running server:

```sh
# Create a 2-player game
GAME=$(curl -s -X POST localhost:8080/api/games \
  -H 'Content-Type: application/json' -d '{"players":2}' | jq -r .id)

# Both players join, capture the returned tokens
TOK_A=$(curl -s -X POST localhost:8080/api/games/$GAME/join \
  -H 'Content-Type: application/json' -d '{"name":"Alice"}' | jq -r .token)
TOK_B=$(curl -s -X POST localhost:8080/api/games/$GAME/join \
  -H 'Content-Type: application/json' -d '{"name":"Bob"}' | jq -r .token)

# Alice plays at the center; tokens authenticate the player
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

A game starts in `waiting` and transitions to `playing` once every seat is claimed. The WebSocket emits one `state` event on connect, then a `move` event after every placement.

### Coordinates

The board uses axial coordinates. The center is `(q=0, r=0)`. A position is on the board iff `|q| ≤ 10`, `|r| ≤ 10`, and `|q + r| ≤ 10`. The three line axes used for captures and alignments are `(1, 0)`, `(0, 1)`, and `(1, -1)`.

The `cells` field of the game DTO is a flat array of length `(2·side − 1)² = 441`, in row-major order over the axial bounding box. Values are `−1` for off-board slots, `0` for empty intersections, and `1..6` for player colors.

## Project layout

```
cmd/server/        main entrypoint
internal/game/     pure engine — no I/O, no concurrency
  types.go         Color, Position, Move, errors
  board.go         hex storage, In/At/Set, three line directions
  rules.go         capture detection and run scanning
  state.go         turn order, win conditions, ApplyMove
internal/server/   HTTP + WebSocket layer
  server.go        REST routes
  store.go         in-memory game registry, seat/token assignment
  hub.go           per-game pub/sub for WebSocket clients
  ws.go            WebSocket handler
  middleware.go    request logging
  cors.go          dev-mode permissive CORS
  dto.go           wire types
```

The `game` package has no dependencies outside the standard library; the `server` package adds `github.com/coder/websocket`.

## Tests

```sh
go test ./...
go test -cover ./internal/game/...   # 99.3 %
```

The engine tests cover hex bounds, capture detection on all three axes, capture chaining (multi-axis and multi-pattern on a single axis), suicide non-capture, alignment counting before and after captures, win conditions for each kind (4, 5, 6 alignments, capture threshold), turn rotation for 2- and 3-player games, and every documented error path. The server tests cover the REST round trip including token-gated moves and the WebSocket broadcast.
