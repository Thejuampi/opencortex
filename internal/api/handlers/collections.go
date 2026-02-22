package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

func (s *Server) CreateCollection(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		ParentID    *string        `json:"parent_id"`
		IsPublic    *bool          `json:"is_public"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	isPublic := true
	if req.IsPublic != nil {
		isPublic = *req.IsPublic
	}
	c, err := s.App.CreateCollection(r.Context(), repos.CreateCollectionInput{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Description: req.Description,
		ParentID:    req.ParentID,
		CreatedBy:   authCtx.Agent.ID,
		IsPublic:    isPublic,
		Metadata:    req.Metadata,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"collection": c}, nil)
}

func (s *Server) ListCollections(w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("per_page"), 50)
	items, total, err := s.App.Store.ListCollections(r.Context(), page, perPage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": items}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) GetCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := s.App.Store.GetCollection(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "collection not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collection": c}, nil)
}

func (s *Server) UpdateCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name        *string        `json:"name"`
		Description *string        `json:"description"`
		ParentID    *string        `json:"parent_id"`
		IsPublic    *bool          `json:"is_public"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	c, err := s.App.Store.UpdateCollection(r.Context(), id, req.Name, req.Description, req.ParentID, req.IsPublic, req.Metadata)
	if err != nil {
		if err.Error() == "collection_cycle" {
			writeErr(w, http.StatusConflict, "CONFLICT", "collection cycle detected")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collection": c}, nil)
}

func (s *Server) DeleteCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.App.Store.DeleteCollection(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}

func (s *Server) CollectionKnowledge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("limit"), 20)
	items, total, err := s.App.Store.ListKnowledgeByCollection(r.Context(), id, page, perPage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": items}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) CollectionTree(w http.ResponseWriter, r *http.Request) {
	tree, err := s.App.Store.CollectionTree(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tree": tree}, nil)
}
