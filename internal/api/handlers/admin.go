package handlers

import (
	"net/http"

	"opencortex/internal/storage"
)

func (s *Server) AdminStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.App.Store.Stats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats}, nil)
}

func (s *Server) AdminConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config
	cfg.Auth.AdminKey = ""
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg}, nil)
}

func (s *Server) AdminBackup(w http.ResponseWriter, r *http.Request) {
	path, err := storage.BackupFile(s.Config.Database.Path, s.Config.Database.BackupPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup_path": path}, nil)
}

func (s *Server) AdminHealth(w http.ResponseWriter, r *http.Request) {
	s.Health(w, r)
}

func (s *Server) AdminVacuum(w http.ResponseWriter, r *http.Request) {
	if _, err := s.DB.ExecContext(r.Context(), "VACUUM"); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vacuumed": true}, nil)
}

func (s *Server) PurgeExpiredMessages(w http.ResponseWriter, r *http.Request) {
	rows, err := s.App.Store.PurgeExpired(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": rows}, nil)
}

func (s *Server) RBACRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.App.Store.ListRolesWithPermissions(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles}, nil)
}

func (s *Server) RBACAssign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AgentID == "" || req.Role == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "agent_id and role are required")
		return
	}
	if err := s.App.Store.AssignRole(r.Context(), req.AgentID, req.Role); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assigned": true}, nil)
}

func (s *Server) RBACRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AgentID == "" || req.Role == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "agent_id and role are required")
		return
	}
	if err := s.App.Store.RevokeRole(r.Context(), req.AgentID, req.Role); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true}, nil)
}
