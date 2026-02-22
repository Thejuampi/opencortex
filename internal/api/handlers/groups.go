package handlers

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

func (s *Server) CreateGroup(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Mode        string         `json:"mode"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "name is required")
		return
	}
	group, err := s.App.Store.CreateGroup(r.Context(), repos.CreateGroupInput{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Description: req.Description,
		Mode:        model.GroupMode(req.Mode),
		CreatedBy:   authCtx.Agent.ID,
		Metadata:    req.Metadata,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"group": group}, nil)
}

func (s *Server) ListGroups(w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("per_page"), 50)
	groups, total, err := s.App.Store.ListGroups(r.Context(), page, perPage, r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups}, &pagination{Page: page, PerPage: perPage, Total: total})
}

func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	group, err := s.App.Store.GetGroupByID(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "group not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group}, nil)
}

func (s *Server) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Description *string        `json:"description"`
		Mode        *string        `json:"mode"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	group, err := s.App.Store.UpdateGroup(r.Context(), id, req.Description, req.Mode, req.Metadata)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group}, nil)
}

func (s *Server) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.App.Store.DeleteGroup(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}

func (s *Server) AddGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	var req struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "agent_id is required")
		return
	}
	if err := s.App.Store.AddGroupMember(r.Context(), groupID, req.AgentID, req.Role); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": true}, nil)
}

func (s *Server) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agent_id")
	if err := s.App.Store.RemoveGroupMember(r.Context(), groupID, agentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true}, nil)
}

func (s *Server) ListGroupMembers(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	members, err := s.App.Store.ListGroupMembers(r.Context(), groupID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members}, nil)
}
