package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexis-marcel/gamekit/auth"
)

// fakeAuth stands in for the host's auth method: "Remote-User: <id>" signs in.
type fakeAuth struct{ guests bool }

func (fakeAuth) User(r *http.Request) *auth.User {
	if id := r.Header.Get("Remote-User"); id != "" {
		return &auth.User{ID: id, Name: "Name of " + id, Email: id + "@example.com"}
	}
	return nil
}

func (a fakeAuth) Info() auth.Info { return auth.Info{Mode: "test", GuestPlay: a.guests} }

func newAuthServer(t *testing.T, a auth.Authenticator) *Server {
	t.Helper()
	srv, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), NewStore(nil), nil, Config{Auth: a})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// seen runs one request through identify and returns the account it attached.
func seen(srv *Server, internal bool, req *http.Request) *AuthUser {
	var got *AuthUser
	srv.identify(internal, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = userFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestIdentify_AnonymousPasses(t *testing.T) {
	srv := newAuthServer(t, nil) // no accounts
	if u := seen(srv, false, httptest.NewRequest("GET", "/", nil)); u != nil {
		t.Fatalf("anonymous request identified as %+v", u)
	}
}

func TestIdentify_AccountGetsStableUUID(t *testing.T) {
	srv := newAuthServer(t, fakeAuth{guests: true})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Remote-User", "alice")
	u := seen(srv, false, req)
	if u == nil || !uuidRe.MatchString(u.ID) || u.DisplayName != "Name of alice" || u.Email != "alice@example.com" {
		t.Fatalf("account = %+v", u)
	}
	if again := seen(srv, false, req); again.ID != u.ID {
		t.Fatalf("same account, different ids: %s then %s", u.ID, again.ID)
	}
	req.Header.Set("Remote-User", "bob")
	if bob := seen(srv, false, req); bob.ID == u.ID {
		t.Fatal("two accounts share one id")
	}
	// A UUID (Authentik, local accounts) passes through, lowercased.
	req.Header.Set("Remote-User", "99343ADF-48BF-465F-893D-88FCB8F6FE9F")
	if v := seen(srv, false, req); v.ID != "99343adf-48bf-465f-893d-88fcb8f6fe9f" {
		t.Fatalf("uuid id rewritten to %s", v.ID)
	}
}

func TestIdentify_InternalTrustsForwardedIdentityOnly(t *testing.T) {
	srv := newAuthServer(t, fakeAuth{guests: true})
	forged := identityValue(&AuthUser{ID: "11111111-2222-4333-8444-555555555555", DisplayName: "Mallory"})

	// Public listener: a client-supplied identity header is ignored.
	pub := httptest.NewRequest("GET", "/", nil)
	pub.Header.Set(identityHeader, forged)
	if u := seen(srv, false, pub); u != nil {
		t.Fatalf("public listener trusted %+v", u)
	}
	// Internal listener: the forwarding pod's identity is the caller's, and
	// the host's auth method is not consulted (the request came from a pod).
	in := httptest.NewRequest("GET", "/", nil)
	in.Header.Set(identityHeader, forged)
	in.Header.Set("Remote-User", "alice")
	if u := seen(srv, true, in); u == nil || u.DisplayName != "Mallory" {
		t.Fatalf("internal listener = %+v", u)
	}
	for _, bad := range []string{"", "not json", `{"id":"alice"}`} {
		in.Header.Set(identityHeader, bad)
		if u := seen(srv, true, in); u != nil {
			t.Errorf("identity %q accepted as %+v", bad, u)
		}
	}
}

func TestProxyToCarriesIdentity(t *testing.T) {
	srv := newAuthServer(t, fakeAuth{guests: true})
	var got string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(identityHeader)
	}))
	defer owner.Close()
	gateway := srv.identify(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.proxyTo(w, r, "g1", "owner", strings.TrimPrefix(owner.URL, "http://"), func() {})
	}))
	req := httptest.NewRequest("POST", "/api/games/g1/moves", nil)
	req.Header.Set("Remote-User", "alice")
	req.Header.Set(identityHeader, "forged by the client")
	gateway.ServeHTTP(httptest.NewRecorder(), req)
	if u := identityFrom(got); u == nil || u.DisplayName != "Name of alice" {
		t.Fatalf("owner received identity %q", got)
	}
	// Anonymous caller: the client's forged header is stripped, not passed on.
	req.Header.Del("Remote-User")
	gateway.ServeHTTP(httptest.NewRecorder(), req)
	if got != "" {
		t.Fatalf("forged identity forwarded: %q", got)
	}
}

func TestGuestPlayOff(t *testing.T) {
	srv := newAuthServer(t, fakeAuth{guests: false})
	h := srv.Routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/games", strings.NewReader(`{"players":2}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
	if !strings.Contains(rec.Body.String(), `"guestPlay":false`) || !strings.Contains(rec.Body.String(), `"user":null`) {
		t.Fatalf("config = %s", rec.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/config", nil)
	req.Header.Set("Remote-User", "alice")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"name":"Name of alice"`) {
		t.Fatalf("signed-in config = %s", rec.Body.String())
	}
}

func TestRequireUser_RejectsAnonymous(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	if u := requireUser(rec, req); u != nil {
		t.Fatalf("requireUser returned %+v on anonymous", u)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
