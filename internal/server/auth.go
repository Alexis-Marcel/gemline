package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// JWT-based user auth is layered on top of (and orthogonal to) the per-seat
// token. The seat token authorizes "this client controls seat X in game Y"
// and travels in the X-Player-Token header. A Supabase JWT authorizes "this
// client is user U" and travels in Authorization: Bearer <jwt>.
//
// Both can be present simultaneously: an authenticated user playing a seat
// they claimed will send both headers on every move.

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

// parseSupabaseJWT verifies an HS256 signature against `secret` and returns
// the claims. Anonymous (unauthenticated) callers should not invoke this —
// they simply skip the auth step.
func parseSupabaseJWT(token, secret string) (*supabaseClaims, error) {
	if secret == "" {
		return nil, errors.New("JWT secret not configured")
	}
	claims := &supabaseClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %s", t.Method.Alg())
		}
		return []byte(secret), nil
	})
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
func jwtMiddleware(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := authorizationBearer(r)
		if token == "" || secret == "" {
			next.ServeHTTP(w, r)
			return
		}
		claims, err := parseSupabaseJWT(token, secret)
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
// Seat-level tokens travel in X-Player-Token; do not look there.
func authorizationBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// playerToken extracts a seat-level token. Falls back to a query param for
// the WebSocket upgrade case where setting headers is awkward.
func playerToken(r *http.Request) string {
	if t := r.Header.Get("X-Player-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}
