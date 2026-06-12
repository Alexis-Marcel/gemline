package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Two orthogonal tokens: a per-seat token in X-Player-Token authorizes
// "controls seat X in game Y"; a Supabase JWT in Authorization: Bearer
// authorizes "is user U". Both can travel on the same request.

type userCtxKey struct{}

type AuthUser struct {
	ID          string
	Email       string
	DisplayName string
}

func userFromContext(ctx context.Context) (*AuthUser, bool) {
	v, ok := ctx.Value(userCtxKey{}).(*AuthUser)
	return v, ok
}

type supabaseClaims struct {
	Email        string                 `json:"email,omitempty"`
	Role         string                 `json:"role,omitempty"`
	UserMetadata map[string]interface{} `json:"user_metadata,omitempty"`
	jwt.RegisteredClaims
}

func displayNameFromMetadata(md map[string]interface{}) string {
	if md == nil {
		return ""
	}
	raw, ok := md["display_name"]
	if !ok {
		return ""
	}
	str, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(str)
}

func jwksKeyfunc(supabaseURL string) (jwt.Keyfunc, error) {
	jwksURL := strings.TrimRight(supabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
	kf, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("init jwks keyfunc: %w", err)
	}
	return kf.Keyfunc, nil
}

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

// testAuthHook is wired only by test builds (auth_testhook_test.go) so server
// tests can authenticate without a real JWT. It is nil in the production binary
// — the *_test.go that sets it is never compiled in — so no header can bypass
// auth in production, whatever the verifier's state.
var testAuthHook func(*http.Request) *AuthUser

// jwtMiddleware attaches an AuthUser when a valid bearer is present but never
// rejects: bad/missing tokens fall through as anonymous so public endpoints
// keep working. Endpoints that need a user call requireUser.
func jwtMiddleware(verifier jwt.Keyfunc, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := authorizationBearer(r)
		if token == "" || verifier == nil {
			if testAuthHook != nil {
				if u := testAuthHook(r); u != nil {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u)))
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		claims, err := parseSupabaseJWT(token, verifier)
		if err != nil {
			if log != nil {
				log.Debug("jwt rejected", "err", err, "path", r.URL.Path)
			}
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey{}, &AuthUser{
			ID:          claims.Subject,
			Email:       claims.Email,
			DisplayName: displayNameFromMetadata(claims.UserMetadata),
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requireUser(w http.ResponseWriter, r *http.Request) *AuthUser {
	u, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil
	}
	return u
}

// authorizationBearer reads the JWT from Authorization, falling back to
// ?access_token= for WebSocket upgrades (browsers can't set headers there).
func authorizationBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("access_token")
}

// playerToken reads the seat token from X-Player-Token, falling back to
// ?token= for WebSocket upgrades.
func playerToken(r *http.Request) string {
	if t := r.Header.Get("X-Player-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}
