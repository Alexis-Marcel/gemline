package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Two orthogonal tokens: a per-seat token in X-Player-Token authorizes
// "controls seat X in game Y"; an access-token JWT in Authorization: Bearer
// authorizes "is user U". Both can travel on the same request. The JWT comes
// from any OIDC provider (OIDC_ISSUER, e.g. Authentik) or, legacy, Supabase.

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

// tokenClaims covers both shapes: standard OIDC (name, preferred_username) and
// Supabase (user_metadata.display_name).
type tokenClaims struct {
	Email             string                 `json:"email,omitempty"`
	Name              string                 `json:"name,omitempty"`
	PreferredUsername string                 `json:"preferred_username,omitempty"`
	Role              string                 `json:"role,omitempty"`
	UserMetadata      map[string]interface{} `json:"user_metadata,omitempty"`
	jwt.RegisteredClaims
}

// displayName prefers Supabase's display_name, then the OIDC name, then the username.
func (c *tokenClaims) displayName() string {
	if raw, ok := c.UserMetadata["display_name"].(string); ok && strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw)
	}
	if n := strings.TrimSpace(c.Name); n != "" {
		return n
	}
	return strings.TrimSpace(c.PreferredUsername)
}

func jwksKeyfunc(supabaseURL string) (jwt.Keyfunc, error) {
	jwksURL := strings.TrimRight(supabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
	kf, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("init jwks keyfunc: %w", err)
	}
	return kf.Keyfunc, nil
}

// oidcKeyfunc discovers the issuer's JWKS and also pins iss (and aud when set):
// a token signed by the same provider for another application is refused.
func oidcKeyfunc(ctx context.Context, issuer, audience string) (jwt.Keyfunc, error) {
	discovery := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery: HTTP %d", res.StatusCode)
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if doc.JWKSURI == "" {
		return nil, errors.New("oidc discovery: no jwks_uri")
	}
	kf, err := keyfunc.NewDefault([]string{doc.JWKSURI})
	if err != nil {
		return nil, fmt.Errorf("init jwks keyfunc: %w", err)
	}
	return pinClaims(kf.Keyfunc, doc.Issuer, audience), nil
}

// pinClaims wraps a Keyfunc so tokens must carry the expected iss/aud; the
// signature itself is still checked by jwt against the returned key.
func pinClaims(kf jwt.Keyfunc, issuer, audience string) jwt.Keyfunc {
	return func(t *jwt.Token) (interface{}, error) {
		if iss, _ := t.Claims.GetIssuer(); iss != issuer {
			return nil, fmt.Errorf("unexpected issuer %q", iss)
		}
		if audience != "" {
			aud, _ := t.Claims.GetAudience()
			if !slices.Contains(aud, audience) {
				return nil, fmt.Errorf("token not issued for %q", audience)
			}
		}
		return kf(t)
	}
}

func parseJWT(token string, verifier jwt.Keyfunc) (*tokenClaims, error) {
	if verifier == nil {
		return nil, errors.New("JWT verifier not configured")
	}
	claims := &tokenClaims{}
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
		claims, err := parseJWT(token, verifier)
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
			DisplayName: claims.displayName(),
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
