package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"opencortex/internal/api"
	"opencortex/internal/api/handlers"
	ws "opencortex/internal/api/websocket"
	"opencortex/internal/broker"
	"opencortex/internal/config"
	"opencortex/internal/service"
	"opencortex/internal/storage"
	"opencortex/internal/storage/repos"
	syncer "opencortex/internal/sync"
)

func TestInitAndAgentLifecycle(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "test.db")
	cfg.Auth.Enabled = true

	ctx := context.Background()
	db, err := storage.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store := repos.New(db)
	mem := broker.NewMemory(64)
	app := service.New(cfg, store, mem)
	_, adminKey, err := app.BootstrapInit(ctx, "admin")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	syncEngine := syncer.NewEngine(db, store)
	handler := handlers.New(app, db, cfg, syncEngine)
	hub := ws.NewHub(app, store)
	router := api.NewRouter(handler, app, hub)
	ts := httptest.NewServer(router)
	defer ts.Close()

	// Health endpoint works without auth.
	resp, err := http.Get(ts.URL + "/api/v1/admin/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Auth-required endpoint must reject missing key.
	resp, err = http.Get(ts.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("agents request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// With key it should return data.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agents authed request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := os.ReadFile(cfg.Database.Path)
		_ = b
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		OK   bool `json:"ok"`
		Data struct {
			Agents []map[string]any `json:"agents"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.OK || len(body.Data.Agents) == 0 {
		t.Fatalf("expected agents in response, got %+v", body)
	}
}
