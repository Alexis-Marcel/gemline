package server

import (
	"encoding/json"
	"net/http"
)

// ProfileDTO is the user-controlled portion of the profile (display name,
// timestamps). Email comes from the JWT and is returned alongside.
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

func normalizeDisplayName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		out = append(out, r)
	}
	// Trim ASCII whitespace.
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
