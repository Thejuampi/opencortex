package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

func (s *Server) CreateTopic(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Retention   string `json:"retention"`
		TTLSeconds  *int   `json:"ttl_seconds"`
		IsPublic    *bool  `json:"is_public"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	isPublic := true
	if req.IsPublic != nil {
		isPublic = *req.IsPublic
	}
	topic, err := s.App.CreateTopic(r.Context(), repos.CreateTopicInput{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Description: req.Description,
		Retention:   model.TopicRetention(req.Retention),
		TTLSeconds:  req.TTLSeconds,
		CreatedBy:   authCtx.Agent.ID,
		IsPublic:    isPublic,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"topic": topic}, nil)
}

func (s *Server) ListTopics(w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("per_page"), 50)
	topics, total, err := s.App.Store.ListTopics(r.Context(), page, perPage, r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": topics}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) GetTopic(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	topic, err := s.App.Store.GetTopicByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "topic not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topic": topic}, nil)
}

func (s *Server) UpdateTopic(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Description *string `json:"description"`
		Retention   *string `json:"retention"`
		TTLSeconds  *int    `json:"ttl_seconds"`
		IsPublic    *bool   `json:"is_public"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	topic, err := s.App.Store.UpdateTopic(r.Context(), id, req.Description, req.Retention, req.TTLSeconds, req.IsPublic)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topic": topic}, nil)
}

func (s *Server) DeleteTopic(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.App.Store.DeleteTopic(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	_ = s.App.Broker.DeleteTopic(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}

func (s *Server) SubscribeTopic(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	canAccess, err := s.App.Store.CanAccessTopic(r.Context(), id, authCtx.Agent.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if !canAccess {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "topic is invite-only")
		return
	}
	var req struct {
		Filter map[string]any `json:"filter"`
	}
	_ = decodeJSON(r, &req)
	if err := s.App.Store.Subscribe(r.Context(), authCtx.Agent.ID, id, req.Filter); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	_, _ = s.App.Broker.Subscribe(r.Context(), authCtx.Agent.ID, id)
	writeJSON(w, http.StatusOK, map[string]any{"subscribed": true}, nil)
}

func (s *Server) UnsubscribeTopic(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.App.Store.Unsubscribe(r.Context(), authCtx.Agent.ID, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	_ = s.App.Broker.Unsubscribe(r.Context(), authCtx.Agent.ID, id)
	writeJSON(w, http.StatusOK, map[string]any{"subscribed": false}, nil)
}

func (s *Server) TopicSubscribers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	subs, err := s.App.Store.ListTopicSubscribers(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscribers": subs}, nil)
}

func (s *Server) TopicMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("limit"), 50)
	msgs, total, err := s.App.Store.ListMessagesByTopic(r.Context(), id, page, perPage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) AddTopicMember(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	topicID := chi.URLParam(r, "id")
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "agent_id is required")
		return
	}
	if err := s.App.Store.AddTopicMember(r.Context(), topicID, req.AgentID, authCtx.Agent.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": true}, nil)
}

func (s *Server) RemoveTopicMember(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agent_id")
	if err := s.App.Store.RemoveTopicMember(r.Context(), topicID, agentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true}, nil)
}

func (s *Server) ListTopicMembers(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "id")
	members, err := s.App.Store.ListTopicMembers(r.Context(), topicID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members}, nil)
}
