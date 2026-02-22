package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

func (s *Server) CreateKnowledge(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req struct {
		Title        string         `json:"title"`
		Content      string         `json:"content"`
		ContentType  string         `json:"content_type"`
		Summary      *string        `json:"summary"`
		Tags         []string       `json:"tags"`
		CollectionID *string        `json:"collection_id"`
		Source       *string        `json:"source"`
		ChangeNote   *string        `json:"change_note"`
		Metadata     map[string]any `json:"metadata"`
		Visibility   string         `json:"visibility"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "title and content are required")
		return
	}
	entry, err := s.App.CreateKnowledge(r.Context(), repos.CreateKnowledgeInput{
		ID:           uuid.NewString(),
		Title:        req.Title,
		Content:      req.Content,
		ContentType:  req.ContentType,
		Summary:      req.Summary,
		Tags:         req.Tags,
		CollectionID: req.CollectionID,
		CreatedBy:    authCtx.Agent.ID,
		Source:       req.Source,
		Metadata:     req.Metadata,
		Visibility:   model.KnowledgeVisibility(req.Visibility),
		ChangeNote:   req.ChangeNote,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"knowledge": entry}, nil)
}

func (s *Server) ListKnowledge(w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("limit"), 20)
	var pinned *bool
	if rawPinned := r.URL.Query().Get("pinned"); rawPinned != "" {
		v := strings.EqualFold(rawPinned, "true") || rawPinned == "1"
		pinned = &v
	}
	entries, total, err := s.App.Store.SearchKnowledge(r.Context(), repos.KnowledgeFilters{
		Query:        r.URL.Query().Get("q"),
		Tags:         splitCSV(r.URL.Query().Get("tags")),
		CollectionID: r.URL.Query().Get("collection_id"),
		CreatedBy:    r.URL.Query().Get("created_by"),
		Since:        r.URL.Query().Get("since"),
		Pinned:       pinned,
		Page:         page,
		PerPage:      perPage,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entries}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) GetKnowledge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entry, err := s.App.Store.GetKnowledge(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "knowledge entry not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entry}, nil)
}

func (s *Server) ReplaceKnowledge(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	var req struct {
		Content     string  `json:"content"`
		ContentType string  `json:"content_type"`
		Summary     *string `json:"summary"`
		ChangeNote  *string `json:"change_note"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	entry, err := s.App.UpdateKnowledgeContent(r.Context(), repos.UpdateKnowledgeContentInput{
		ID:          id,
		Content:     req.Content,
		Summary:     req.Summary,
		UpdatedBy:   authCtx.Agent.ID,
		ChangeNote:  req.ChangeNote,
		ContentType: req.ContentType,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entry}, nil)
}

func (s *Server) PatchKnowledge(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	var req struct {
		Summary      *string        `json:"summary"`
		Tags         []string       `json:"tags"`
		CollectionID *string        `json:"collection_id"`
		Source       *string        `json:"source"`
		Metadata     map[string]any `json:"metadata"`
		Visibility   *string        `json:"visibility"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	entry, err := s.App.Store.PatchKnowledgeMetadata(r.Context(), id, req.Summary, req.Tags, req.CollectionID, req.Source, req.Metadata, authCtx.Agent.ID, req.Visibility)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entry}, nil)
}

func (s *Server) DeleteKnowledge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.App.Store.DeleteKnowledge(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}

func (s *Server) KnowledgeHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	history, err := s.App.Store.KnowledgeHistory(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": history}, nil)
}

func (s *Server) KnowledgeVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	v, err := strconv.Atoi(chi.URLParam(r, "v"))
	if err != nil || v <= 0 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid version")
		return
	}
	version, err := s.App.Store.KnowledgeVersion(r.Context(), id, v)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "version not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version}, nil)
}

func (s *Server) RestoreKnowledge(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	v, err := strconv.Atoi(chi.URLParam(r, "v"))
	if err != nil || v <= 0 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid version")
		return
	}
	var req struct {
		ChangeNote *string `json:"change_note"`
	}
	_ = decodeJSON(r, &req)
	entry, err := s.App.Store.RestoreKnowledgeVersion(r.Context(), id, v, authCtx.Agent.ID, req.ChangeNote)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entry}, nil)
}

func (s *Server) PinKnowledge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entry, err := s.App.Store.SetKnowledgePinned(r.Context(), id, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entry}, nil)
}

func (s *Server) UnpinKnowledge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entry, err := s.App.Store.SetKnowledgePinned(r.Context(), id, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": entry}, nil)
}

func splitCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
