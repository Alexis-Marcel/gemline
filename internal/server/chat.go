package server

import (
	"encoding/json"
	"errors"
	"net/http"
)

type postMessageRequest struct {
	Body string `json:"body"`
}

func (s *Server) postChat(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")
	token := playerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "seat token required to post")
		return
	}
	var req postMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	msg, err := s.store.PostMessage(r.Context(), gameID, token, req.Body)
	if err != nil {
		writeError(w, statusForChatError(err), err.Error())
		return
	}

	s.events.Publish(gameID, eventChat(*msg))
	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.store.MessagesForGame(r.Context(), r.PathValue("id"), 200)
	if err != nil {
		s.log.Error("load messages", "err", err)
		writeError(w, http.StatusInternalServerError, "could not load messages")
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func statusForChatError(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrEmptyMessage):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
