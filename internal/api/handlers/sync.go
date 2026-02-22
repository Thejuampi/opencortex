package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"opencortex/internal/model"
	"opencortex/internal/storage/repos"
	syncer "opencortex/internal/sync"
)

func (s *Server) ListRemotes(w http.ResponseWriter, r *http.Request) {
	remotes, err := s.App.Store.ListRemotes(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"remotes": remotes}, nil)
}

func (s *Server) AddRemote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string   `json:"name"`
		URL              string   `json:"url"`
		APIKey           string   `json:"api_key"`
		Direction        string   `json:"direction"`
		Scope            string   `json:"scope"`
		ScopeIDs         []string `json:"scope_ids"`
		ConflictStrategy string   `json:"conflict_strategy"`
		Schedule         *string  `json:"schedule"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	manifest, err := s.App.AddRemote(r.Context(), repos.CreateRemoteInput{
		ID:         uuid.NewString(),
		RemoteURL:  req.URL,
		RemoteName: req.Name,
		Direction:  model.SyncDirection(req.Direction),
		Scope:      model.SyncScope(req.Scope),
		ScopeIDs:   req.ScopeIDs,
		Strategy:   req.ConflictStrategy,
		Schedule:   req.Schedule,
	}, req.APIKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"remote": manifest}, nil)
}

func (s *Server) DeleteRemote(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.App.Store.DeleteRemote(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
}

func (s *Server) Push(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Remote        string   `json:"remote"`
		Scope         string   `json:"scope"`
		CollectionIDs []string `json:"collection_ids"`
		TopicIDs      []string `json:"topic_ids"`
		Force         bool     `json:"force"`
		APIKey        string   `json:"api_key"`
		Items         []any    `json:"items"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.Remote == "" && len(req.Items) > 0 {
		// Peer node push payload received.
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "count": len(req.Items)}, nil)
		return
	}
	ids := req.CollectionIDs
	if len(ids) == 0 {
		ids = req.TopicIDs
	}
	log, err := s.SyncEngine.Push(r.Context(), req.Remote, req.APIKey, model.SyncScope(req.Scope), ids)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sync_log": log}, nil)
}

func (s *Server) Pull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Remote        string   `json:"remote"`
		Scope         string   `json:"scope"`
		CollectionIDs []string `json:"collection_ids"`
		TopicIDs      []string `json:"topic_ids"`
		Force         bool     `json:"force"`
		APIKey        string   `json:"api_key"`
		Items         []any    `json:"items"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if req.Remote == "" && len(req.Items) > 0 {
		scope := model.SyncScope(req.Scope)
		if scope == "" {
			scope = model.SyncScopeFull
		}
		items, err := syncer.BuildManifest(r.Context(), s.DB, scope, nil)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items}, nil)
		return
	}
	ids := req.CollectionIDs
	if len(ids) == 0 {
		ids = req.TopicIDs
	}
	log, err := s.SyncEngine.Pull(r.Context(), req.Remote, req.APIKey, model.SyncScope(req.Scope), ids)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sync_log": log}, nil)
}

func (s *Server) SyncStatus(w http.ResponseWriter, r *http.Request) {
	logs, err := s.App.Store.ListSyncLogs(r.Context(), 1)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	var latest any = nil
	if len(logs) > 0 {
		latest = logs[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"latest": latest,
	}, nil)
}

func (s *Server) SyncLogs(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 100)
	logs, err := s.App.Store.ListSyncLogs(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs}, nil)
}

func (s *Server) SyncDiff(w http.ResponseWriter, r *http.Request) {
	// Inbound diff for peer nodes.
	if r.Method == http.MethodPost {
		var req struct {
			Scope string                `json:"scope"`
			Items []syncer.ManifestItem `json:"items"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
			return
		}
		need, have, err := s.SyncEngine.Diff(r.Context(), model.SyncScope(req.Scope), nil, req.Items)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"need": need, "have": have}, nil)
		return
	}

	// GET preview for local operator.
	remote := r.URL.Query().Get("remote")
	scope := model.SyncScope(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = model.SyncScopeFull
	}
	manifest, err := s.App.Store.GetRemote(r.Context(), remote)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "remote not found")
		return
	}
	items, err := syncer.BuildManifest(r.Context(), s.DB, scope, manifest.ScopeIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items}, nil)
}

func (s *Server) ListConflicts(w http.ResponseWriter, r *http.Request) {
	conflicts, err := s.App.Store.ListOpenConflicts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": conflicts}, nil)
}

func (s *Server) ResolveConflict(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Strategy string `json:"strategy"`
		Note     string `json:"note"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	if err := s.SyncEngine.ResolveConflict(r.Context(), id, req.Strategy, req.Note); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": true}, nil)
}

func (s *Server) InboundPush(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	// For strict v1 the receiving endpoint acknowledges and lets importer path process payloads in follow-up version.
	writeJSON(w, http.StatusOK, map[string]any{"received": true, "items": req["items"]}, nil)
}

func (s *Server) InboundPull(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return
	}
	// For strict v1 this endpoint returns current local manifest subset.
	scope := model.SyncScope("full")
	if raw, ok := req["scope"].(string); ok && raw != "" {
		scope = model.SyncScope(raw)
	}
	items, err := syncer.BuildManifest(r.Context(), s.DB, scope, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items}, nil)
}
