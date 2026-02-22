package handlers

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
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
		ToGroupID        *string        `json:"to_group_id"`
		QueueMode        bool           `json:"queue_mode"`
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
		ToGroupID:   req.ToGroupID,
		QueueMode:   req.QueueMode,
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

func (s *Server) BroadcastMessage(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		ContentType      string         `json:"content_type"`
		Content          string         `json:"content"`
		Priority         string         `json:"priority"`
		Tags             []string       `json:"tags"`
		Metadata         map[string]any `json:"metadata"`
		Type             string         `json:"type"`
		ExpiresInSeconds int            `json:"expires_in_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "content is required")
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	if strings.TrimSpace(req.Type) != "" {
		req.Metadata["type"] = strings.TrimSpace(req.Type)
	}
	var expiresAt *time.Time
	if req.ExpiresInSeconds > 0 {
		t := time.Now().UTC().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
		expiresAt = &t
	}
	msg, err := s.App.CreateBroadcastMessage(r.Context(), repos.CreateMessageInput{
		ID:          uuid.NewString(),
		FromAgentID: authCtx.Agent.ID,
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

	filters := repos.GetInboxFilters{
		TopicID:       r.URL.Query().Get("topic_id"),
		FromAgentID:   r.URL.Query().Get("from_agent_id"),
		Priority:      r.URL.Query().Get("priority"),
		Limit:         parseInt(r.URL.Query().Get("limit"), 50),
		MarkDelivered: r.URL.Query().Get("peek") != "true",
		LeaseSeconds:  parseInt(r.URL.Query().Get("lease_seconds"), 300),
		IncludeRead:   r.URL.Query().Get("all") == "true",
		IncludeDead:   r.URL.Query().Get("dead") == "true",
	}
	cursor := r.URL.Query().Get("cursor")
	waitSec := parseInt(r.URL.Query().Get("wait"), 0)

	start := time.Now()
	for {
		msgs, newCursor, err := s.App.GetInboxAsync(r.Context(), authCtx.Agent.ID, cursor, filters)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		if len(msgs) > 0 || waitSec <= 0 || time.Since(start).Seconds() >= float64(waitSec) {
			writeJSON(w, http.StatusOK, map[string]any{
				"messages": msgs,
				"cursor":   newCursor,
			}, nil)
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

func (s *Server) Ack(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		IDs  []string `json:"ids"`
		UpTo string   `json:"up_to"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}

	affected, err := s.App.AckMessages(r.Context(), authCtx.Agent.ID, req.IDs, req.UpTo)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"acked": affected}, nil)
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

func (s *Server) ClaimMessages(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		Limit        int    `json:"limit"`
		TopicID      string `json:"topic_id"`
		FromAgentID  string `json:"from_agent_id"`
		Priority     string `json:"priority"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	claims, err := s.App.Store.ClaimMessages(r.Context(), repos.ClaimMessagesInput{
		AgentID:      authCtx.Agent.ID,
		Limit:        req.Limit,
		TopicID:      req.TopicID,
		FromAgentID:  req.FromAgentID,
		Priority:     req.Priority,
		LeaseSeconds: s.normalizeLease(req.LeaseSeconds),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"claims": claims}, nil)
}

func (s *Server) AckMessageClaim(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	messageID := chi.URLParam(r, "id")
	var req struct {
		ClaimToken string `json:"claim_token"`
		MarkRead   *bool  `json:"mark_read"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.ClaimToken == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "claim_token is required")
		return
	}
	markRead := true
	if req.MarkRead != nil {
		markRead = *req.MarkRead
	}
	if err := s.App.Store.AckMessageClaim(r.Context(), messageID, authCtx.Agent.ID, req.ClaimToken, markRead); err != nil {
		if errors.Is(err, repos.ErrClaimNotFound) {
			writeErr(w, http.StatusConflict, "CLAIM_NOT_FOUND", "claim not found or expired")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": messageID, "acked": true, "mark_read": markRead}, nil)
}

func (s *Server) NackMessageClaim(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	messageID := chi.URLParam(r, "id")
	var req struct {
		ClaimToken string `json:"claim_token"`
		Reason     string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.ClaimToken == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "claim_token is required")
		return
	}
	if err := s.App.Store.NackMessageClaim(r.Context(), messageID, authCtx.Agent.ID, req.ClaimToken, req.Reason); err != nil {
		if errors.Is(err, repos.ErrClaimNotFound) {
			writeErr(w, http.StatusConflict, "CLAIM_NOT_FOUND", "claim not found or expired")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": messageID, "nacked": true}, nil)
}

func (s *Server) RenewMessageClaim(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	messageID := chi.URLParam(r, "id")
	var req struct {
		ClaimToken   string `json:"claim_token"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.ClaimToken == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "claim_token is required")
		return
	}
	expiresAt, err := s.App.Store.RenewMessageClaim(r.Context(), messageID, authCtx.Agent.ID, req.ClaimToken, s.normalizeLease(req.LeaseSeconds))
	if err != nil {
		if errors.Is(err, repos.ErrClaimNotFound) {
			writeErr(w, http.StatusConflict, "CLAIM_NOT_FOUND", "claim not found or expired")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": messageID, "claim_expires_at": expiresAt}, nil)
}

func (s *Server) normalizeLease(in int) int {
	lease := in
	if lease <= 0 {
		lease = s.Config.MCP.Delivery.DefaultLeaseSeconds
	}
	if lease <= 0 {
		lease = 300
	}
	maxLease := s.Config.MCP.Delivery.MaxLeaseSeconds
	if maxLease > 0 && lease > maxLease {
		lease = maxLease
	}
	return lease
}
