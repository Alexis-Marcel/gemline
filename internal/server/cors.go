package server

import "net/http"

// corsMiddleware echoes the request Origin if it matches one of the
// allow-list entries (production mode). When `allowed` is empty/nil, falls
// back to `*` — convenient for local dev but unsafe for any deployment that
// uses cookies or relies on the Origin header as an authentication signal.
func corsMiddleware(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Player-Token")

		origin := r.Header.Get("Origin")
		if len(allowed) == 0 {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" && originAllowed(allowed, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(allowed []string, origin string) bool {
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}
