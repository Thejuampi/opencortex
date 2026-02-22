package handlers

import (
	"net"
	"net/http"

	"opencortex/internal/bootstrap"
)

func (s *Server) BootstrapStatus(w http.ResponseWriter, r *http.Request) {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	if !isLoopback(remoteHost) {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "bootstrap status is only available for local connections")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": bootstrap.CurrentStatus()}, nil)
}
