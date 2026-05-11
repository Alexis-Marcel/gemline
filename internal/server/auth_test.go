package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testJWTSecret = "test-secret-do-not-use-in-prod"

func makeJWT(t *testing.T, secret, sub, email string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, supabaseClaims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWTMiddleware_AnonymousPasses(t *testing.T) {
	called := false
	handler := jwtMiddleware(testJWTSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := userFromContext(r.Context()); ok {
			t.Error("expected no user in context for anonymous request")
		}
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler not called")
	}
}

func TestJWTMiddleware_ValidTokenSetsUser(t *testing.T) {
	want := "abc-123"
	called := false
	handler := jwtMiddleware(testJWTSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFromContext(r.Context())
		if !ok || u.ID != want {
			t.Errorf("user in context = %+v, want id=%s", u, want)
		}
		called = true
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT(t, testJWTSecret, want, "a@b.c"))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("handler not called")
	}
}

func TestJWTMiddleware_BadSignatureFallsBackToAnonymous(t *testing.T) {
	handler := jwtMiddleware(testJWTSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := userFromContext(r.Context()); ok {
			t.Error("user in context despite bad signature")
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT(t, "wrong-secret", "abc", "a@b.c"))
	handler.ServeHTTP(httptest.NewRecorder(), req)
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
