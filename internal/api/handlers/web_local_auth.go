package handlers

import (
	"net"
	"net/http"
	"strings"
)

const webLocalAdminFingerprint = "opencortex-webui-local-admin-v1"

// WebLocalAdminAuth is a localhost-only endpoint that auto-registers a durable
// web UI identity and elevates it to admin for zero-ceremony local dashboards.
func (s *Server) WebLocalAdminAuth(w http.ResponseWriter, r *http.Request) {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	if !isLoopback(remoteHost) {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "local web auth is only available for local connections")
		return
	}

	agent, rawKey, err := s.App.AutoRegisterLocal(r.Context(), "web-admin", webLocalAdminFingerprint)
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "local web auth failed")
		return
	}
	if err := s.App.Store.AssignRole(r.Context(), agent.ID, "admin"); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "failed to assign admin role")
		return
	}
	roles, err := s.App.Store.AgentRoles(r.Context(), agent.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "failed to read roles")
		return
	}

	// Keep web identity tagged as admin for discoverability in UI.
	tags := append([]string{}, agent.Tags...)
	hasAdminTag := false
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), "admin") {
			hasAdminTag = true
			break
		}
	}
	if !hasAdminTag {
		tags = append(tags, "admin")
		if updated, err := s.App.Store.UpdateAgent(r.Context(), agent.ID, nil, tags, nil, nil); err == nil {
			agent = updated
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent":   agent,
		"api_key": rawKey,
		"roles":   roles,
	}, nil)
}
