package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signHS256(t *testing.T, claims tokenClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func claimsFor(iss string, aud ...string) tokenClaims {
	return tokenClaims{RegisteredClaims: jwt.RegisteredClaims{
		Subject:   "5e2907fd-661b-4f4b-bb2c-c72c631dfbfb",
		Issuer:    iss,
		Audience:  aud,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
}

func TestPinClaims(t *testing.T) {
	kf := pinClaims(testHS256Keyfunc(testJWTSecret), "https://idp.test/app/", "gemline")
	cases := []struct {
		name   string
		claims tokenClaims
		ok     bool
	}{
		{"bon émetteur et audience", claimsFor("https://idp.test/app/", "gemline"), true},
		{"autre émetteur", claimsFor("https://evil.test/", "gemline"), false},
		{"autre application", claimsFor("https://idp.test/app/", "autre"), false},
		{"sans audience", claimsFor("https://idp.test/app/"), false},
	}
	for _, c := range cases {
		_, err := parseJWT(signHS256(t, c.claims), kf)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestTokenClaimsDisplayName(t *testing.T) {
	cases := []struct {
		claims tokenClaims
		want   string
	}{
		{tokenClaims{UserMetadata: map[string]interface{}{"display_name": " Emma "}, Name: "x"}, "Emma"},
		{tokenClaims{Name: "Loïc", PreferredUsername: "loic"}, "Loïc"},
		{tokenClaims{PreferredUsername: "justin"}, "justin"},
		{tokenClaims{}, ""},
	}
	for _, c := range cases {
		if got := c.claims.displayName(); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.claims, got, c.want)
		}
	}
}

// fakeOIDC serves a discovery document and a one-key RSA JWKS, like Authentik.
func fakeOIDC(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/app/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL + "/app/", "jwks_uri": srv.URL + "/app/jwks/"})
	})
	mux.HandleFunc("/app/jwks/", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOIDCKeyfunc_VerifiesProviderTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := fakeOIDC(t, key)
	kf, err := oidcKeyfunc(context.Background(), idp.URL+"/app/", "gemline")
	if err != nil {
		t.Fatal(err)
	}
	sign := func(c tokenClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	good := claimsFor(idp.URL+"/app/", "gemline")
	good.Name = "Alexis"
	got, err := parseJWT(sign(good), kf)
	if err != nil {
		t.Fatalf("valid provider token rejected: %v", err)
	}
	if got.Subject != good.Subject || got.displayName() != "Alexis" {
		t.Fatalf("claims = %+v", got)
	}
	if _, err := parseJWT(sign(claimsFor(idp.URL+"/app/", "autre")), kf); err == nil {
		t.Fatal("token for another application accepted")
	}
	if _, err := parseJWT(signHS256(t, good), kf); err == nil {
		t.Fatal("HS256 token accepted by the RSA JWKS verifier")
	}
}

func TestOIDCKeyfunc_DiscoveryFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := oidcKeyfunc(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("expected an error when discovery is missing")
	}
}
