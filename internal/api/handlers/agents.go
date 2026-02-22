package handlers

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

func (s *Server) CreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string          `json:"name"`
		Type        model.AgentType `json:"type"`
		Description string          `json:"description"`
		Tags        []string        `json:"tags"`
		Metadata    map[string]any  `json:"metadata"`
		Role        string          `json:"role"`
		KeyKind     string          `json:"key_kind"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "name is required")
		return
	}
	if req.Type == "" {
		req.Type = model.AgentTypeAI
	}
	agent, key, err := s.App.CreateAgent(r.Context(), repos.CreateAgentInput{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Type:        req.Type,
		Description: req.Description,
		Tags:        req.Tags,
		Metadata:    req.Metadata,
		Status:      model.AgentStatusActive,
	}, safeKeyKind(req.KeyKind), req.Role)
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent":   agent,
		"api_key": key,
	}, nil)
}

func (s *Server) ListAgents(w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("per_page"), 50)
	agents, total, err := s.App.Store.ListAgents(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("q"), page, perPage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) GetAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, err := s.App.Store.GetAgentByID(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "agent not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agent}, nil)
}

func (s *Server) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Description *string        `json:"description"`
		Tags        []string       `json:"tags"`
		Metadata    map[string]any `json:"metadata"`
		Status      *string        `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	agent, err := s.App.Store.UpdateAgent(r.Context(), id, req.Description, req.Tags, req.Metadata, req.Status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agent}, nil)
}

func (s *Server) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.App.Store.DeactivateAgent(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "inactive"}, nil)
}

func (s *Server) RotateAgentKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		KeyKind string `json:"key_kind"`
	}
	_ = decodeJSON(r, &req)
	key, err := s.App.RotateAgentKey(r.Context(), id, safeKeyKind(req.KeyKind))
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": key}, nil)
}

func (s *Server) CurrentAgent(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": authCtx.Agent, "roles": authCtx.Roles}, nil)
}

func (s *Server) AgentMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("limit"), 50)
	msgs, total, err := s.App.Store.ListAgentMessages(r.Context(), id, page, perPage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) AgentTopics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	topics, err := s.App.Store.ListAgentSubscriptions(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": topics}, nil)
}

func safeKeyKind(v string) string {
	switch strings.TrimSpace(v) {
	case "test", "remote":
		return strings.TrimSpace(v)
	default:
		return "live"
	}
}
