package server

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/alexis-marcel/gamekit/auth"
)

// Two orthogonal credentials: a per-seat token in X-Player-Token authorizes
// "controls seat X in game Y"; the account (gamekit/auth, mode chosen by the
// host: none, oidc, proxy, local) says "is user U". Both can travel on the
// same request, and an account is never required to play unless the host
// sets GUEST_PLAY=false.

type userCtxKey struct{}

type AuthUser struct {
	ID          string // always a UUID (profiles, seats and ratings key on it)
	Email       string
	DisplayName string
}

func userFromContext(ctx context.Context) (*AuthUser, bool) {
	v, ok := ctx.Value(userCtxKey{}).(*AuthUser)
	return v, ok
}

// testAuthHook is wired only by test builds (auth_testhook_test.go) so server
// tests can authenticate without a real provider. It is nil in the production
// binary — the *_test.go that sets it is never compiled in — so no header can
// bypass auth in production.
var testAuthHook func(*http.Request) *AuthUser

// identityHeader carries the caller's account from the pod that authenticated
// it to the game's owner. It is read on the internal listener only — the same
// unreachability from outside that makes markForwarded trustworthy — and is
// overwritten by proxyTo, so a client cannot inject it either way.
const identityHeader = "X-Gemline-Identity"

// identify attaches the caller's account when there is one but never rejects:
// guests fall through so public endpoints keep working; endpoints that need a
// user call requireUser. Public listener: the host's auth method decides.
// Internal listener: the identity the forwarding pod already resolved.
func (s *Server) identify(internal bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u *AuthUser
		switch {
		case internal:
			u = identityFrom(r.Header.Get(identityHeader))
		case testAuthHook != nil && testAuthHook(r) != nil:
			u = testAuthHook(r)
		default:
			if a := s.auth.User(r); a != nil {
				u = &AuthUser{ID: accountUUID(a.ID), Email: a.Email, DisplayName: a.Name}
			}
		}
		if u != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u))
		}
		next.ServeHTTP(w, r)
	})
}

type identity struct {
	ID    string `json:"id"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

func identityValue(u *AuthUser) string {
	b, _ := json.Marshal(identity{ID: u.ID, Email: u.Email, Name: u.DisplayName})
	return string(b)
}

func identityFrom(v string) *AuthUser {
	if v == "" {
		return nil
	}
	var id identity
	if err := json.Unmarshal([]byte(v), &id); err != nil || !uuidRe.MatchString(id.ID) {
		return nil
	}
	return &AuthUser{ID: id.ID, Email: id.Email, DisplayName: id.Name}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// accountNamespace scopes the UUIDs derived from non-UUID account ids.
var accountNamespace = [16]byte{0x5c, 0x0d, 0x1e, 0x4f, 0x8a, 0x3b, 0x4c, 0x6d, 0x9e, 0x2f, 0x70, 0x81, 0x92, 0xa3, 0xb4, 0xc5}

// accountUUID maps an account id to the UUID every user_id column expects:
// a UUID passes through (OIDC providers like Authentik, the local mode); any
// other id (a reverse proxy's username, a numeric OIDC sub) becomes a stable
// UUIDv5, so the same account always lands on the same profile.
func accountUUID(id string) string {
	if l := strings.ToLower(id); uuidRe.MatchString(l) {
		return l
	}
	h := sha1.New()
	h.Write(accountNamespace[:])
	h.Write([]byte(id))
	b := h.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func requireUser(w http.ResponseWriter, r *http.Request) *AuthUser {
	u, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil
	}
	return u
}

// guestAllowed refuses an anonymous caller when the host requires an account
// to play (GUEST_PLAY=false).
func (s *Server) guestAllowed(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := userFromContext(r.Context()); ok || s.auth.Info().GuestPlay {
		return true
	}
	writeError(w, http.StatusUnauthorized, "an account is required to play here")
	return false
}

// getConfig tells the frontend how to sign in (the host's auth method) and,
// when the server is the one that knows (proxy, local), who is signed in.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	resp := struct {
		Auth auth.Info `json:"auth"`
		User *identity `json:"user"`
	}{Auth: s.auth.Info()}
	if u, ok := userFromContext(r.Context()); ok {
		resp.User = &identity{ID: u.ID, Name: u.DisplayName}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// playerToken reads the seat token from X-Player-Token, falling back to
// ?token= for WebSocket upgrades.
func playerToken(r *http.Request) string {
	if t := r.Header.Get("X-Player-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}
