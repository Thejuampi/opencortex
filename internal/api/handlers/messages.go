package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

func (s *Server) CreateMessage(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		ToAgentID        *string        `json:"to_agent_id"`
		TopicID          *string        `json:"topic_id"`
		ReplyToID        *string        `json:"reply_to_id"`
		ContentType      string         `json:"content_type"`
		Content          string         `json:"content"`
		Priority         string         `json:"priority"`
		Tags             []string       `json:"tags"`
		Metadata         map[string]any `json:"metadata"`
		ExpiresInSeconds int            `json:"expires_in_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.Content == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "content is required")
		return
	}
	var expiresAt *time.Time
	if req.ExpiresInSeconds > 0 {
		t := time.Now().UTC().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
		expiresAt = &t
	}
	msg, err := s.App.CreateMessage(r.Context(), repos.CreateMessageInput{
		ID:          uuid.NewString(),
		FromAgentID: authCtx.Agent.ID,
		ToAgentID:   req.ToAgentID,
		TopicID:     req.TopicID,
		ReplyToID:   req.ReplyToID,
		ContentType: req.ContentType,
		Content:     req.Content,
		Priority:    model.MessagePriority(req.Priority),
		Tags:        req.Tags,
		Metadata:    req.Metadata,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message": msg}, nil)
}

func (s *Server) Inbox(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("limit"), 50)
	msgs, total, err := s.App.Store.ListInbox(r.Context(), authCtx.Agent.ID, repos.MessageFilters{
		Status:      r.URL.Query().Get("status"),
		TopicID:     r.URL.Query().Get("topic_id"),
		FromAgentID: r.URL.Query().Get("from_agent_id"),
		Since:       r.URL.Query().Get("since"),
		Priority:    r.URL.Query().Get("priority"),
		Page:        page,
		PerPage:     perPage,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) GetMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	msg, err := s.App.Store.GetMessageByID(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "message not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": msg}, nil)
}

func (s *Server) MarkRead(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.App.Store.MarkMessageRead(r.Context(), id, authCtx.Agent.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "read"}, nil)
}

func (s *Server) MessageThread(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	thread, err := s.App.Store.MessageThread(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": thread}, nil)
}

func (s *Server) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.App.Store.DeleteMessage(r.Context(), id, authCtx.Agent.ID); err != nil {
		if err.Error() == "sender_only" {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "sender only")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}
