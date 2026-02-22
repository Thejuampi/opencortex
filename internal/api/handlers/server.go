package handlers

import (
	"database/sql"
	"net/http"

	"opencortex/internal/config"
	"opencortex/internal/service"
	syncer "opencortex/internal/sync"
)

type Server struct {
	App        *service.App
	DB         *sql.DB
	Config     config.Config
	SyncEngine *syncer.Engine
}

func New(app *service.App, db *sql.DB, cfg config.Config, syncEngine *syncer.Engine) *Server {
	return &Server{
		App:        app,
		DB:         db,
		Config:     cfg,
		SyncEngine: syncEngine,
	}
}

func (s *Server) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"mode":   s.Config.Mode,
	}, nil)
}
