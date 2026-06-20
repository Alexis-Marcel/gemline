package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/alexis-marcel/gemline/internal/elo"
	"github.com/alexis-marcel/gemline/internal/game"
)

// This file is compiled only into the test binary, so none of the synchronous
// matchmaking fixture below ships in the production server. Tests seat players
// in one HTTP call via POST /api/games/matchmake; the route is wired through the
// testRoutes hook (nil in prod, so the prod router never mounts it) and the
// handler plus its Store helpers live here, keeping the shipped binary free of a
// second, prod-unused matchmaking path. Production matchmaking is the async
// queue (POST /api/matchmake/enqueue + /ws/lobby) driven by matcherTick.
func init() {
	testRoutes = func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc("POST /api/games/matchmake", s.matchmakeGame)
	}
}

// matchmakeRequest is the body of POST /api/games/matchmake. Tests use
// players=2 (1v1) or 4 (multi); the handler accepts any 2..6.
type matchmakeRequest struct {
	Players int `json:"players"`
}

// matchmakeGame synchronously seats the caller into a matching game (creating
// one if none fit), bypassing the queue + matcher + Postgres so server tests
// stay hermetic.
func (s *Server) matchmakeGame(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var req matchmakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Players < 2 || req.Players > game.MaxPlayers {
		writeError(w, http.StatusBadRequest, "players must be in [2, 6]")
		return
	}
	rec, err := s.store.Matchmake(r.Context(), req.Players, u.ID)
	if err != nil {
		s.log.Error("matchmake", "err", err)
		writeError(w, http.StatusInternalServerError, "could not matchmake")
		return
	}
	name := s.displayNameFor(r.Context(), u)
	seat, token, err := s.store.Join(r.Context(), rec.ID, name, u.ID, -1)
	if err != nil {
		writeError(w, statusForJoinError(err), err.Error())
		return
	}
	rec.Lock()
	dto := toGameDTO(rec)
	rec.Unlock()
	s.events.Publish(rec.ID, eventState(dto))
	writeJSON(w, http.StatusOK, joinResponse{Game: dto, Seat: toSeatDTO(seat), Token: token})
}

// Matchmake returns a joinable public waiting game (excludeUserID not already
// seated), scoring 1v1 by age-widened Elo proximity and multi by most-filled /
// oldest-first; creates a fresh game if none match.
func (s *Store) Matchmake(ctx context.Context, players int, excludeUserID string) (*GameRecord, error) {
	candidates, err := s.repo.LobbyGames(ctx, 50)
	if err != nil {
		return nil, err
	}
	if candidates == nil {
		candidates = s.scanLobbyCache(50)
	}
	ratingMode := RatingModeMulti
	if players == 2 {
		ratingMode = RatingMode1v1
	}
	callerRating := s.fetchCallerRating(ctx, excludeUserID, ratingMode)

	type scored struct {
		rec    *GameRecord
		delta  float64
		age    time.Duration
		seated int
	}
	var picks []scored
	now := time.Now()
	for _, c := range candidates {
		if c.Players != players || c.Seated >= c.Players {
			continue
		}
		rec, ok, err := s.Get(ctx, c.GameID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rec.Lock()
		joinable := rec.Status == StatusWaiting && !rec.AllSeated()
		if joinable && excludeUserID != "" {
			for _, seat := range rec.Seats {
				if seat.UserID == excludeUserID {
					joinable = false
					break
				}
			}
		}
		var occupantUserIDs []string
		seated := 0
		if joinable {
			for _, seat := range rec.Seats {
				if seat.Occupied {
					seated++
					if seat.UserID != "" {
						occupantUserIDs = append(occupantUserIDs, seat.UserID)
					}
				}
			}
		}
		rec.Unlock()
		if !joinable {
			continue
		}

		age := now.Sub(c.CreatedAt)
		avg := s.averageRating(ctx, occupantUserIDs, ratingMode)
		delta := math.Abs(float64(callerRating) - float64(avg))

		if players == 2 && !withinBand(callerRating, avg, age) {
			continue
		}
		picks = append(picks, scored{rec: rec, delta: delta, age: age, seated: seated})
	}
	if len(picks) > 0 {
		var best scored
		bestIdx := -1
		for i, p := range picks {
			if bestIdx == -1 {
				best, bestIdx = p, i
				continue
			}
			// 1v1: smallest rating delta. Multi: most-filled, age tiebreak.
			better := false
			if players == 2 {
				better = p.delta < best.delta
			} else {
				if p.seated != best.seated {
					better = p.seated > best.seated
				} else {
					better = p.age > best.age
				}
			}
			if better {
				best, bestIdx = p, i
			}
		}
		_ = bestIdx
		return best.rec, nil
	}
	return s.Create(ctx, players, VisibilityPublic)
}

// fetchCallerRating resolves a user's Elo for mode, defaulting when they have
// no rated games (or no user).
func (s *Store) fetchCallerRating(ctx context.Context, userID, mode string) int {
	if userID == "" {
		return elo.DefaultRating
	}
	r, err := s.repo.RatingFor(ctx, userID, mode)
	if err != nil || r.Games == 0 {
		return elo.DefaultRating
	}
	return r.Rating
}

// averageRating returns the mean Elo of users in mode, unrated taking the
// default. Empty list returns the default.
func (s *Store) averageRating(ctx context.Context, userIDs []string, mode string) int {
	if len(userIDs) == 0 {
		return elo.DefaultRating
	}
	ratings, err := s.repo.RatingsFor(ctx, userIDs, mode)
	if err != nil {
		return elo.DefaultRating
	}
	sum := 0
	for _, r := range ratings {
		if r.Games == 0 {
			sum += elo.DefaultRating
		} else {
			sum += r.Rating
		}
	}
	return sum / len(ratings)
}

// scanLobbyCache returns public waiting games from the in-memory cache,
// most-recent-first. Used by Matchmake under a noop repo (hermetic tests),
// where the cache is authoritative.
func (s *Store) scanLobbyCache(limit int) []LobbyEntry {
	s.mu.Lock()
	candidates := make([]*GameRecord, 0, len(s.games))
	for _, rec := range s.games {
		candidates = append(candidates, rec)
	}
	s.mu.Unlock()

	out := make([]LobbyEntry, 0)
	for _, rec := range candidates {
		rec.Lock()
		if rec.Status == StatusWaiting && rec.Visibility == VisibilityPublic {
			seated := 0
			for _, st := range rec.Seats {
				if st.Occupied {
					seated++
				}
			}
			out = append(out, LobbyEntry{
				GameID:    rec.ID,
				Players:   len(rec.Seats),
				Seated:    seated,
				CreatedAt: rec.CreatedAt,
			})
		}
		rec.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// withinBand reports whether two ratings are close enough for the given
// candidate age. Wraps the prod scoreBandFor; used only by the test fixture and
// matchmaking_test.go.
func withinBand(callerRating, candidateRating int, age time.Duration) bool {
	delta := math.Abs(float64(callerRating) - float64(candidateRating))
	return delta <= scoreBandFor(age)
}
