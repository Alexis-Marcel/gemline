package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// JWT-based user auth is layered on top of (and orthogonal to) the per-seat
// token. The seat token authorizes "this client controls seat X in game Y"
// and travels in the X-Player-Token header. A Supabase JWT authorizes "this
// client is user U" and travels in Authorization: Bearer <jwt>.
//
// Both can be present simultaneously: an authenticated user playing a seat
// they claimed will send both headers on every move.
//
// Verification uses Supabase's JWT Signing Keys (asymmetric ES256/RS256).
// The keyfunc library fetches the JWKS from
// <SUPABASE_URL>/auth/v1/.well-known/jwks.json, caches it, and refreshes
// on key rotation in the background.

type userCtxKey struct{}

// AuthUser is what the JWT middleware attaches to the request context for
// authenticated requests. It holds only what the JWT claims: the Supabase
// user UUID and the email.
type AuthUser struct {
	ID    string
	Email string
}

func userFromContext(ctx context.Context) (*AuthUser, bool) {
	v, ok := ctx.Value(userCtxKey{}).(*AuthUser)
	return v, ok
}

// supabaseClaims models the subset of the Supabase JWT we care about.
type supabaseClaims struct {
	Email string `json:"email,omitempty"`
	Role  string `json:"role,omitempty"`
	jwt.RegisteredClaims
}

// jwksKeyfunc returns a Keyfunc that validates tokens against the JWKS
// published by Supabase. Pass the project URL (e.g. https://<id>.supabase.co);
// the JWKS path is appended automatically.
func jwksKeyfunc(supabaseURL string) (jwt.Keyfunc, error) {
	jwksURL := strings.TrimRight(supabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
	kf, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("init jwks keyfunc: %w", err)
	}
	return kf.Keyfunc, nil
}

// parseSupabaseJWT verifies the token using `verifier` and returns the
// claims. Anonymous (unauthenticated) callers should not invoke this —
// they simply skip the auth step.
func parseSupabaseJWT(token string, verifier jwt.Keyfunc) (*supabaseClaims, error) {
	if verifier == nil {
		return nil, errors.New("JWT verifier not configured")
	}
	claims := &supabaseClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, verifier)
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.Subject == "" {
		return nil, errors.New("missing subject")
	}
	return claims, nil
}

// jwtMiddleware decodes the Authorization bearer (if any) and stores the
// resulting AuthUser in the request context. It NEVER rejects the request
// on missing/invalid JWT — endpoints that require auth check the context
// themselves via requireUser. This way public endpoints (game CRUD,
// anonymous join) keep working without a token.
func jwtMiddleware(verifier jwt.Keyfunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := authorizationBearer(r)
		if token == "" || verifier == nil {
			// Hermetic-test back door: when auth is disabled (no SUPABASE_URL),
			// honour an X-Test-User-ID header so server tests can exercise
			// auth-requiring endpoints without standing up a real IdP. In
			// production the verifier is non-nil so this code never runs.
			if verifier == nil {
				if id := r.Header.Get("X-Test-User-ID"); id != "" {
					ctx := context.WithValue(r.Context(), userCtxKey{}, &AuthUser{
						ID:    id,
						Email: id + "@test.local",
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		claims, err := parseSupabaseJWT(token, verifier)
		if err != nil {
			// Bad/expired token: behave as if anonymous rather than 401-ing
			// every public endpoint. Endpoints that need a user surface their
			// own 401 via requireUser.
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey{}, &AuthUser{
			ID:    claims.Subject,
			Email: claims.Email,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireUser returns the authenticated user or writes a 401 and returns nil.
func requireUser(w http.ResponseWriter, r *http.Request) *AuthUser {
	u, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil
	}
	return u
}

// authorizationBearer extracts a JWT from the Authorization header.
// Falls back to ?access_token= for the WebSocket upgrade case, where
// browsers can't set custom headers on the upgrade request — the lobby
// WS in particular requires auth and has no other way to surface the
// caller's identity. Seat-level tokens travel in X-Player-Token; do
// not look there.
func authorizationBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("access_token")
}

// playerToken extracts a seat-level token. Falls back to a query param for
// the WebSocket upgrade case where setting headers is awkward.
func playerToken(r *http.Request) string {
	if t := r.Header.Get("X-Player-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}
