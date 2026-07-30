// Package bus is the Redis pub/sub fan-out with per-game routing: a pod
// subscribes to game:{id} only while it has local interest in that game
// (cached record or WS spectator), so its inbound traffic scales with what it
// serves, not with the whole site.
//
// The bus is lossy by design: subscriptions are fire-and-forget, and
// consumers reconcile from the canonical store (seq gaps → catch-up).
// go-redis re-subscribes all tracked channels automatically after a
// reconnect, so the loss window is the reconnect gap.
package bus

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	lobbyChannel    = "lobby"
	gameChannelPfx  = "game:"
	connectTimeout  = 5 * time.Second
	publishTimeout  = 5 * time.Second
)

func gameChannel(gameID string) string { return gameChannelPfx + gameID }

// Redis is a Bus implementation over Redis pub/sub. Safe for concurrent use
// after New; handlers must be registered before Start.
type Redis struct {
	client *redis.Client
	log    *slog.Logger

	onGame  func(payload []byte)
	onLobby func(payload []byte)

	mu     sync.Mutex
	refs   map[string]int // gameID → local interest count
	pubsub *redis.PubSub

	cancel context.CancelFunc
	done   chan struct{}
}

func NewRedis(url string, log *slog.Logger) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis url: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &Redis{
		client: client,
		log:    log,
		refs:   make(map[string]int),
	}, nil
}

// OnGameEvent registers the handler for game:{id} messages. The envelope
// carries the gameID, so the handler needs no channel name.
func (r *Redis) OnGameEvent(fn func(payload []byte)) { r.onGame = fn }

// OnLobby registers the handler for the lobby broadcast channel.
func (r *Redis) OnLobby(fn func(payload []byte)) { r.onLobby = fn }

func (r *Redis) PublishGame(ctx context.Context, gameID string, payload []byte) error {
	return r.client.Publish(ctx, gameChannel(gameID), payload).Err()
}

func (r *Redis) PublishLobby(ctx context.Context, payload []byte) error {
	return r.client.Publish(ctx, lobbyChannel, payload).Err()
}

// WatchGame declares local interest in gameID: the 0→1 transition subscribes
// the shared PubSub connection to its channel. Refcounted — the store (cached
// record) and the hub (first WS spectator) each count as one interest.
func (r *Redis) WatchGame(gameID string) {
	r.mu.Lock()
	r.refs[gameID]++
	first := r.refs[gameID] == 1
	ps := r.pubsub
	r.mu.Unlock()
	if first && ps != nil {
		if err := ps.Subscribe(context.Background(), gameChannel(gameID)); err != nil {
			r.log.Warn("bus subscribe failed", "game", gameID, "err", err)
		}
	}
}

// UnwatchGame drops one interest; the 1→0 transition unsubscribes.
func (r *Redis) UnwatchGame(gameID string) {
	r.mu.Lock()
	n, ok := r.refs[gameID]
	if !ok {
		r.mu.Unlock()
		return
	}
	n--
	last := n <= 0
	if last {
		delete(r.refs, gameID)
	} else {
		r.refs[gameID] = n
	}
	ps := r.pubsub
	r.mu.Unlock()
	if last && ps != nil {
		if err := ps.Unsubscribe(context.Background(), gameChannel(gameID)); err != nil {
			r.log.Warn("bus unsubscribe failed", "game", gameID, "err", err)
		}
	}
}

// Start opens the subscriber connection and launches the receive loop. Games
// watched before Start are subscribed on open.
func (r *Redis) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})

	r.mu.Lock()
	channels := []string{lobbyChannel}
	for id := range r.refs {
		channels = append(channels, gameChannel(id))
	}
	r.pubsub = r.client.Subscribe(runCtx, channels...)
	ps := r.pubsub
	r.mu.Unlock()

	go func() {
		defer close(r.done)
		ch := ps.Channel()
		for {
			select {
			case <-runCtx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				r.dispatch(msg.Channel, []byte(msg.Payload))
			}
		}
	}()
	r.log.Info("bus listening", "driver", "redis", "watched", len(channels)-1)
}

// dispatch runs handlers on the single receive goroutine; handlers must not
// block.
func (r *Redis) dispatch(channel string, payload []byte) {
	switch {
	case channel == lobbyChannel:
		if r.onLobby != nil {
			r.onLobby(payload)
		}
	case strings.HasPrefix(channel, gameChannelPfx):
		if r.onGame != nil {
			r.onGame(payload)
		}
	}
}

func (r *Redis) Close() error {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
	r.mu.Lock()
	ps := r.pubsub
	r.mu.Unlock()
	if ps != nil {
		_ = ps.Close()
	}
	return r.client.Close()
}
