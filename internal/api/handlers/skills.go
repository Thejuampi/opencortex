package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/service"
	skillmeta "opencortex/internal/skills"
	skillinstall "opencortex/internal/skills/install"
	"opencortex/internal/storage/repos"
)

var runSkillInstall = skillinstall.Install

func (s *Server) CreateSkill(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	var req struct {
		Title        string                `json:"title"`
		Content      string                `json:"content"`
		ContentType  string                `json:"content_type"`
		Summary      *string               `json:"summary"`
		Tags         []string              `json:"tags"`
		CollectionID *string               `json:"collection_id"`
		Source       *string               `json:"source"`
		ChangeNote   *string               `json:"change_note"`
		Metadata     map[string]any        `json:"metadata"`
		Visibility   string                `json:"visibility"`
		Slug         string                `json:"slug"`
		Install      skillmeta.InstallSpec `json:"install"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "title and content are required")
		return
	}

	slugIn := req.Slug
	if strings.TrimSpace(slugIn) == "" {
		slugIn = req.Title
	}
	slug, err := skillmeta.NormalizeSlug(slugIn)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	install, err := skillmeta.ValidateInstallSpec(req.Install)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if err := ensureSkillSlugUnique(r.Context(), s.App.Store, slug, ""); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	entry, err := s.App.CreateKnowledge(r.Context(), repos.CreateKnowledgeInput{
		ID:           uuid.NewString(),
		Title:        req.Title,
		Content:      req.Content,
		ContentType:  req.ContentType,
		Summary:      req.Summary,
		Tags:         skillmeta.BuildReservedTags(req.Tags, slug),
		CollectionID: req.CollectionID,
		CreatedBy:    authCtx.Agent.ID,
		Source:       req.Source,
		Metadata:     skillmeta.BuildSkillMetadata(req.Metadata, slug, install),
		Visibility:   model.KnowledgeVisibility(req.Visibility),
		ChangeNote:   req.ChangeNote,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	view, err := skillmeta.ToView(entry)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"skill": view}, nil)
}

func (s *Server) ListSkills(w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	perPage := parseInt(r.URL.Query().Get("limit"), 20)
	var pinned *bool
	if rawPinned := r.URL.Query().Get("pinned"); rawPinned != "" {
		v := strings.EqualFold(rawPinned, "true") || rawPinned == "1"
		pinned = &v
	}

	userTags := splitCSV(r.URL.Query().Get("tags"))
	filters := repos.KnowledgeFilters{
		Query:        r.URL.Query().Get("q"),
		Tags:         append(userTags, skillmeta.TagSpecialSkillset),
		CollectionID: r.URL.Query().Get("collection_id"),
		CreatedBy:    r.URL.Query().Get("created_by"),
		Since:        r.URL.Query().Get("since"),
		Pinned:       pinned,
		Page:         page,
		PerPage:      perPage,
	}
	entries, total, err := s.App.Store.SearchKnowledge(r.Context(), filters)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	out := make([]skillmeta.SkillView, 0, len(entries))
	for _, entry := range entries {
		view, err := skillmeta.ToView(entry)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		out = append(out, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{"skills": out}, &pagination{
		Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Server) GetSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, view, err := s.getSkillByID(r.Context(), id)
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": view}, nil)
}

func (s *Server) ReplaceSkill(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	currentEntry, currentView, err := s.getSkillByID(r.Context(), id)
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	var req struct {
		Content      string                  `json:"content"`
		ContentType  string                  `json:"content_type"`
		Summary      *string                 `json:"summary"`
		ChangeNote   *string                 `json:"change_note"`
		Tags         []string                `json:"tags"`
		CollectionID *string                 `json:"collection_id"`
		Source       *string                 `json:"source"`
		Metadata     map[string]any          `json:"metadata"`
		Visibility   *string                 `json:"visibility"`
		Slug         *string                 `json:"slug"`
		Install      *skillmeta.InstallPatch `json:"install"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "content is required")
		return
	}

	slug := currentView.Slug
	if req.Slug != nil {
		slug, err = skillmeta.NormalizeSlug(*req.Slug)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
	}
	install, err := skillmeta.ApplyInstallPatch(currentView.Install, req.Install)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if err := ensureSkillSlugUnique(r.Context(), s.App.Store, slug, id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	updated, err := s.App.UpdateKnowledgeContent(r.Context(), repos.UpdateKnowledgeContentInput{
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

	baseTags := currentEntry.Tags
	if req.Tags != nil {
		baseTags = req.Tags
	}
	baseMetadata := updated.Metadata
	if req.Metadata != nil {
		baseMetadata = req.Metadata
	}
	patched, err := s.App.Store.PatchKnowledgeMetadata(
		r.Context(),
		id,
		nil,
		skillmeta.BuildReservedTags(baseTags, slug),
		req.CollectionID,
		req.Source,
		skillmeta.BuildSkillMetadata(baseMetadata, slug, install),
		authCtx.Agent.ID,
		req.Visibility,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	view, err := skillmeta.ToView(patched)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": view}, nil)
}

func (s *Server) PatchSkill(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := service.AuthFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	currentEntry, currentView, err := s.getSkillByID(r.Context(), id)
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	var req struct {
		Summary      *string                 `json:"summary"`
		Tags         []string                `json:"tags"`
		CollectionID *string                 `json:"collection_id"`
		Source       *string                 `json:"source"`
		Metadata     map[string]any          `json:"metadata"`
		Visibility   *string                 `json:"visibility"`
		Slug         *string                 `json:"slug"`
		Install      *skillmeta.InstallPatch `json:"install"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}

	slug := currentView.Slug
	if req.Slug != nil {
		slug, err = skillmeta.NormalizeSlug(*req.Slug)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
	}
	install, err := skillmeta.ApplyInstallPatch(currentView.Install, req.Install)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if err := ensureSkillSlugUnique(r.Context(), s.App.Store, slug, id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	baseTags := currentEntry.Tags
	if req.Tags != nil {
		baseTags = req.Tags
	}
	baseMetadata := currentEntry.Metadata
	if req.Metadata != nil {
		baseMetadata = req.Metadata
	}
	patched, err := s.App.Store.PatchKnowledgeMetadata(
		r.Context(),
		id,
		req.Summary,
		skillmeta.BuildReservedTags(baseTags, slug),
		req.CollectionID,
		req.Source,
		skillmeta.BuildSkillMetadata(baseMetadata, slug, install),
		authCtx.Agent.ID,
		req.Visibility,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	view, err := skillmeta.ToView(patched)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": view}, nil)
}

func (s *Server) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, _, err := s.getSkillByID(r.Context(), id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if err := s.App.Store.DeleteKnowledge(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}

func (s *Server) SkillHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, _, err := s.getSkillByID(r.Context(), id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	history, err := s.App.Store.KnowledgeHistory(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": history}, nil)
}

func (s *Server) SkillVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, _, err := s.getSkillByID(r.Context(), id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
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

func (s *Server) PinSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, _, err := s.getSkillByID(r.Context(), id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	entry, err := s.App.Store.SetKnowledgePinned(r.Context(), id, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	view, err := skillmeta.ToView(entry)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": view}, nil)
}

func (s *Server) UnpinSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, _, err := s.getSkillByID(r.Context(), id); err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	entry, err := s.App.Store.SetKnowledgePinned(r.Context(), id, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	view, err := skillmeta.ToView(entry)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": view}, nil)
}

func (s *Server) InstallSkill(w http.ResponseWriter, r *http.Request) {
	_, view, err := s.getSkillByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	var req struct {
		Target   string `json:"target"`
		Platform string `json:"platform"`
		Force    bool   `json:"force"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	req.Target = strings.ToLower(strings.TrimSpace(req.Target))
	if req.Target == "" {
		req.Target = "global"
	}
	if req.Target != "global" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "target must be global for server install")
		return
	}
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	if req.Platform == "" {
		req.Platform = "all"
	}

	result, err := runSkillInstall(r.Context(), skillinstall.Request{
		Slug:     view.Slug,
		Install:  view.Install,
		Target:   req.Target,
		Platform: req.Platform,
		Force:    req.Force,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result}, nil)
}

func (s *Server) getSkillByID(ctx context.Context, id string) (model.KnowledgeEntry, skillmeta.SkillView, error) {
	entry, err := s.App.Store.GetKnowledge(ctx, id)
	if err != nil {
		return model.KnowledgeEntry{}, skillmeta.SkillView{}, mapStoreSkillErr(err)
	}
	view, err := skillmeta.ToView(entry)
	if err != nil {
		if errors.Is(err, skillmeta.ErrNotSkillset) {
			return model.KnowledgeEntry{}, skillmeta.SkillView{}, fmt.Errorf("%w: skill not found", service.ErrNotFound)
		}
		return model.KnowledgeEntry{}, skillmeta.SkillView{}, fmt.Errorf("%w: %v", service.ErrValidation, err)
	}
	return entry, view, nil
}

func ensureSkillSlugUnique(ctx context.Context, store *repos.Store, slug, exceptID string) error {
	page := 1
	const perPage = 100
	for {
		matches, total, err := store.SearchKnowledge(ctx, repos.KnowledgeFilters{
			Tags:    []string{skillmeta.TagSpecialSkillset, skillmeta.TagSkillSlugPrefix + slug},
			Page:    page,
			PerPage: perPage,
		})
		if err != nil {
			return err
		}
		for _, m := range matches {
			if m.ID != exceptID {
				return fmt.Errorf("%w: slug already exists", service.ErrConflict)
			}
		}
		if len(matches) == 0 || page*perPage >= total {
			break
		}
		page++
	}
	return nil
}

func mapStoreSkillErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: skill not found", service.ErrNotFound)
	}
	return err
}
