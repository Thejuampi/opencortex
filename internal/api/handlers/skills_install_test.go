package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"opencortex/internal/broker"
	"opencortex/internal/config"
	"opencortex/internal/service"
	skillmeta "opencortex/internal/skills"
	skillinstall "opencortex/internal/skills/install"
	"opencortex/internal/storage"
	"opencortex/internal/storage/repos"
	syncer "opencortex/internal/sync"
)

func TestInstallSkillHandlerRejectsNonGlobalTarget(t *testing.T) {
	srv, skillID := newSkillsTestServer(t)

	orig := runSkillInstall
	defer func() { runSkillInstall = orig }()
	var called atomic.Bool
	runSkillInstall = func(context.Context, skillinstall.Request) (skillinstall.Result, error) {
		called.Store(true)
		return skillinstall.Result{}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/"+skillID+"/install", bytes.NewBufferString(`{"target":"repo"}`))
	req = withSkillIDParam(req, skillID)
	rec := httptest.NewRecorder()
	srv.InstallSkill(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if called.Load() {
		t.Fatalf("installer should not be called for non-global target")
	}
}

func TestInstallSkillHandlerReturnsResult(t *testing.T) {
	srv, skillID := newSkillsTestServer(t)

	orig := runSkillInstall
	defer func() { runSkillInstall = orig }()
	runSkillInstall = func(_ context.Context, req skillinstall.Request) (skillinstall.Result, error) {
		if req.Target != "global" {
			t.Fatalf("expected global target, got %s", req.Target)
		}
		if req.Slug != "pr-review" {
			t.Fatalf("unexpected slug %s", req.Slug)
		}
		return skillinstall.Result{
			Skill:         "pr-review",
			CanonicalPath: "C:/Users/test/.agents/skills/pr-review",
			Projections: map[string]string{
				"codex": "C:/Users/test/.codex/skills/pr-review",
			},
			Warnings: []string{"copilot projection failed: permission denied"},
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/"+skillID+"/install", bytes.NewBufferString(`{"platform":"all","force":false}`))
	req = withSkillIDParam(req, skillID)
	rec := httptest.NewRecorder()
	srv.InstallSkill(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Result skillinstall.Result `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !env.OK {
		t.Fatalf("expected ok envelope")
	}
	if env.Data.Result.Skill != "pr-review" {
		t.Fatalf("unexpected result skill: %s", env.Data.Result.Skill)
	}
	if env.Data.Result.CanonicalPath == "" {
		t.Fatalf("expected canonical path")
	}
}

func newSkillsTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "skills-install.db")
	cfg.Auth.Enabled = true

	ctx := context.Background()
	db, err := storage.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store := repos.New(db)
	app := serviceForSkillsTest(cfg, store)
	admin, _, err := app.BootstrapInit(ctx, "admin")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	entry, err := app.CreateKnowledge(ctx, repos.CreateKnowledgeInput{
		ID:        "skill-1",
		Title:     "PR Review Skill",
		Content:   "# skill",
		Tags:      skillmeta.BuildReservedTags(nil, "pr-review"),
		CreatedBy: admin.ID,
		Metadata: skillmeta.BuildSkillMetadata(nil, "pr-review", skillmeta.InstallSpec{
			Repo:   "owner/repo",
			Path:   "skills/pr-review",
			Ref:    "main",
			Method: "auto",
		}),
	})
	if err != nil {
		t.Fatalf("create skill knowledge: %v", err)
	}

	return New(app, db, cfg, syncer.NewEngine(db, store)), entry.ID
}

func serviceForSkillsTest(cfg config.Config, store *repos.Store) *service.App {
	return service.New(cfg, store, broker.NewMemory(64))
}

func withSkillIDParam(req *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
