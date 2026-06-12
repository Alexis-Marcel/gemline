package server

import "net/http"

// Wire the X-Test-User-ID auth shortcut for tests only. This file is a *_test.go,
// so it is compiled into the test binary and never into the production server —
// the header has no effect in a shipped build.
func init() {
	testAuthHook = func(r *http.Request) *AuthUser {
		id := r.Header.Get("X-Test-User-ID")
		if id == "" {
			return nil
		}
		return &AuthUser{ID: id, Email: id + "@test.local"}
	}
}
