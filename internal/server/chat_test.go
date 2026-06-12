package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memChatRepo adds in-memory chat retention over noopRepo so the chat handlers
// can run end-to-end without a real Postgres.
type memChatRepo struct {
	noopRepo
	mu   sync.Mutex
	msgs []Message
	next int64
}

func (r *memChatRepo) AppendMessage(_ context.Context, m *Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	m.ID = r.next
	m.SentAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.msgs = append(r.msgs, *m)
	return nil
}

func (r *memChatRepo) MessagesForGame(_ context.Context, gameID string, limit int) ([]Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, 0, len(r.msgs))
	for _, m := range r.msgs {
		if m.GameID == gameID {
			out = append(out, m)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func newChatTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStore(&memChatRepo{}).WithBotDelay(0)
	srv, err := New(log, store, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return httptest.NewServer(srv.Routes())
}

func postChatMessage(t *testing.T, ts *httptest.Server, gameID, token, body string) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/messages", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Player-Token", token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getChatMessages(t *testing.T, ts *httptest.Server, gameID string) []Message {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/api/games/" + gameID + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getChat status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var msgs []Message
	if err := json.Unmarshal(body, &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	return msgs
}

func TestPostChat_RejectsAnonymous(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)

	resp := postChatMessage(t, ts, g.ID, "", "hello")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for no token, got %d", resp.StatusCode)
	}
}

func TestPostChat_RejectsBadToken(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)

	resp := postChatMessage(t, ts, g.ID, "not-a-real-token", "hello")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for bad token, got %d", resp.StatusCode)
	}
}

func TestPostChat_RejectsEmptyBody(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGame(t, ts, g.ID, "Alice", nil)

	for _, body := range []string{"", "   ", "\n\t  "} {
		resp := postChatMessage(t, ts, g.ID, a.Token, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body=%q want 400, got %d", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestPostChat_AcceptsAndPersists(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGame(t, ts, g.ID, "Alice", nil)

	resp := postChatMessage(t, ts, g.ID, a.Token, "gg")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var m Message
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Body != "gg" || m.SeatIndex != a.Seat.Index {
		t.Fatalf("unexpected message round-trip: %+v", m)
	}

	msgs := getChatMessages(t, ts, g.ID)
	if len(msgs) != 1 || msgs[0].Body != "gg" {
		t.Fatalf("getChat want one 'gg', got %+v", msgs)
	}
}

func TestPostChat_TrimsBodyAndCaps(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGame(t, ts, g.ID, "Alice", nil)

	long := strings.Repeat("x", MaxMessageLength+500)
	resp := postChatMessage(t, ts, g.ID, a.Token, "   "+long+"   ")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var m Message
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if len(m.Body) != MaxMessageLength {
		t.Fatalf("expected body capped to %d, got len=%d", MaxMessageLength, len(m.Body))
	}
	if strings.HasPrefix(m.Body, " ") || strings.HasSuffix(m.Body, " ") {
		t.Fatalf("expected trimmed body, got %q…", m.Body[:10])
	}
}

func TestPostChat_404ForUnknownGame(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGame(t, ts, g.ID, "Alice", nil)

	resp := postChatMessage(t, ts, "does-not-exist", a.Token, "hi")
	defer resp.Body.Close()
	// Unknown game + foreign token: either ErrGameNotFound or auth may fire first.
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 404 or 401 for unknown game, got %d", resp.StatusCode)
	}
}

func TestGetChat_EmptyOnFreshGame(t *testing.T) {
	ts := newChatTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	if msgs := getChatMessages(t, ts, g.ID); len(msgs) != 0 {
		t.Fatalf("want empty msg list, got %d", len(msgs))
	}
}
