package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// AutoRegister is a localhost-only endpoint that auto-registers an agent
// without requiring an API key. Remote connections receive 403.
func (s *Server) AutoRegister(w http.ResponseWriter, r *http.Request) {
	// Only allow loopback connections.
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	// Respect X-Real-IP only when the config explicitly trusts it — for now
	// we purposely ignore it so remote proxies cannot fake local access.
	if !isLoopback(remoteHost) {
		writeErr(w, http.StatusForbidden, "FORBIDDEN",
			"auto-registration is only available for local connections")
		return
	}

	var body struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "local-agent"
	}

	agent, rawKey, err := s.App.AutoRegisterLocal(r.Context(), name, strings.TrimSpace(body.Fingerprint))
	if err != nil {
		if mapServiceErr(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "auto-registration failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent":   agent,
		"api_key": rawKey,
	}, nil)
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return host == "localhost"
	}
	return ip.IsLoopback()
}
