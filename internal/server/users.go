package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type ProfileDTO struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type putProfileRequest struct {
	DisplayName string `json:"displayName"`
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	p, err := s.store.Profile(r.Context(), u.ID)
	if err != nil {
		s.log.Error("load profile", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load profile")
		return
	}
	// Lazy first-time profile creation so the leaderboard's INNER JOIN doesn't
	// drop users who never set a name. EnsureProfile won't overwrite an existing one.
	if p == nil {
		fallback := s.displayNameFor(r.Context(), u)
		if err := s.store.EnsureProfile(r.Context(), u.ID, fallback); err != nil {
			s.log.Error("ensure profile", "user", u.ID, "err", err)
		} else {
			p = &Profile{UserID: u.ID, DisplayName: fallback}
		}
	}
	displayName := ""
	if p != nil {
		displayName = p.DisplayName
	}
	writeJSON(w, http.StatusOK, ProfileDTO{
		UserID:      u.ID,
		Email:       u.Email,
		DisplayName: displayName,
	})
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var req putProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := normalizeDisplayName(req.DisplayName)
	if name == "" {
		writeError(w, http.StatusBadRequest, "displayName cannot be empty")
		return
	}
	if err := s.store.UpsertProfile(r.Context(), u.ID, name); err != nil {
		s.log.Error("upsert profile", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save profile")
		return
	}
	writeJSON(w, http.StatusOK, ProfileDTO{
		UserID:      u.ID,
		Email:       u.Email,
		DisplayName: name,
	})
}

func (s *Server) getMyGames(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	games, err := s.store.GamesForUser(r.Context(), u.ID, 50)
	if err != nil {
		s.log.Error("user games", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load history")
		return
	}
	writeJSON(w, http.StatusOK, games)
}

// getPublicProfile serves a read-only view of another player's profile. No auth
// — the response is public, same as the leaderboard.
func (s *Server) getPublicProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "userId required")
		return
	}
	p, err := s.store.Repo().PublicProfile(r.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			writeError(w, http.StatusNotFound, "profile not found")
			return
		}
		s.log.Error("public profile", "user", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load profile")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// searchProfiles serves user search for invites. Auth-gated to stop
// unauthenticated scraping of the user table.
func (s *Server) searchProfiles(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	entries, err := s.store.Repo().SearchProfiles(r.Context(), q, limit)
	if err != nil {
		s.log.Error("search profiles", "err", err)
		writeError(w, http.StatusInternalServerError, "could not search profiles")
		return
	}
	if entries == nil {
		entries = []ProfileSearchEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) getMyStats(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	stats, err := s.store.StatsForUser(r.Context(), u.ID)
	if err != nil {
		s.log.Error("user stats", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// getLeaderboard returns the top rated players for a mode (1v1 or multi).
// Anonymous; ?limit= caps the response, ?mode= defaults to 1v1.
func (s *Server) getLeaderboard(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = RatingMode1v1
	}
	if mode != RatingMode1v1 && mode != RatingModeMulti {
		writeError(w, http.StatusBadRequest, "mode must be '1v1' or 'multi'")
		return
	}
	entries, err := s.store.Leaderboard(r.Context(), mode, limit)
	if err != nil {
		s.log.Error("leaderboard", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load leaderboard")
		return
	}
	if entries == nil {
		entries = []LeaderboardEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func normalizeDisplayName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		out = append(out, r)
	}
	for len(out) > 0 && out[0] == ' ' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return string(out)
}
