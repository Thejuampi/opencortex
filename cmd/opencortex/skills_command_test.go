package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	skillmeta "opencortex/internal/skills"
)

func TestResolveSkillSelectorByID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/skills/skill-1":
			writeTestEnvelope(w, http.StatusOK, true, map[string]any{
				"skill": map[string]any{
					"id":    "skill-1",
					"title": "Skill One",
					"slug":  "skill-one",
					"install": map[string]any{
						"repo":   "openai/skills",
						"path":   "skills/.curated/skill-one",
						"ref":    "main",
						"method": "auto",
					},
				},
			}, nil)
			return
		default:
			writeTestEnvelope(w, http.StatusNotFound, false, nil, map[string]any{
				"code":    "NOT_FOUND",
				"message": "not found",
			})
		}
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "")
	got, err := resolveSkillSelector(client, "skill-1")
	if err != nil {
		t.Fatalf("resolveSkillSelector: %v", err)
	}
	if got.ID != "skill-1" || got.Slug != "skill-one" {
		t.Fatalf("unexpected skill %+v", got)
	}
}

func TestResolveSkillSelectorBySlugFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/skills/my-skill":
			writeTestEnvelope(w, http.StatusNotFound, false, nil, map[string]any{
				"code":    "NOT_FOUND",
				"message": "not found",
			})
			return
		case r.URL.Path == "/api/v1/skills" && strings.Contains(r.URL.RawQuery, "q=my-skill"):
			writeTestEnvelope(w, http.StatusOK, true, map[string]any{
				"skills": []map[string]any{
					{
						"id":    "skill-123",
						"title": "My Skill",
						"slug":  "my-skill",
						"install": map[string]any{
							"repo": "openai/skills",
							"path": "skills/.curated/my-skill",
						},
					},
					{
						"id":    "skill-999",
						"title": "Another",
						"slug":  "another",
						"install": map[string]any{
							"repo": "openai/skills",
							"path": "skills/.curated/another",
						},
					},
				},
			}, nil)
			return
		default:
			writeTestEnvelope(w, http.StatusNotFound, false, nil, map[string]any{
				"code":    "NOT_FOUND",
				"message": "not found",
			})
		}
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "")
	got, err := resolveSkillSelector(client, "my-skill")
	if err != nil {
		t.Fatalf("resolveSkillSelector: %v", err)
	}
	if got.ID != "skill-123" {
		t.Fatalf("unexpected resolved id %s", got.ID)
	}
}

func TestResolveSkillSelectorRequiresExactSlugOrTitle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/skills/my":
			writeTestEnvelope(w, http.StatusNotFound, false, nil, map[string]any{
				"code":    "NOT_FOUND",
				"message": "not found",
			})
			return
		case r.URL.Path == "/api/v1/skills" && strings.Contains(r.URL.RawQuery, "q=my"):
			writeTestEnvelope(w, http.StatusOK, true, map[string]any{
				"skills": []map[string]any{
					{
						"id":    "skill-123",
						"title": "My Skill",
						"slug":  "my-skill",
						"install": map[string]any{
							"repo": "openai/skills",
							"path": "skills/.curated/my-skill",
						},
					},
				},
			}, nil)
			return
		default:
			writeTestEnvelope(w, http.StatusNotFound, false, nil, map[string]any{
				"code":    "NOT_FOUND",
				"message": "not found",
			})
		}
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "")
	if _, err := resolveSkillSelector(client, "my"); err == nil {
		t.Fatalf("expected exact-match selector error")
	} else if !strings.Contains(err.Error(), "not found by exact id, slug, or title") {
		t.Fatalf("unexpected selector error: %v", err)
	}
}

func TestResolveGitRootOrCWDUsesGitRoot(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, string(out))
	}
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	root, err := resolveGitRootOrCWD()
	if err != nil {
		t.Fatalf("resolveGitRootOrCWD: %v", err)
	}
	if filepath.Clean(root) != filepath.Clean(repo) {
		t.Fatalf("expected %s, got %s", repo, root)
	}
}

func TestInstallSkillLocallyRequiresForceForExistingCanonicalPath(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, string(out))
	}
	existing := filepath.Join(repo, ".agents", "skills", "my-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}

	_, err = installSkillLocally(context.Background(), cliSkill{
		ID:      "s1",
		Slug:    "my-skill",
		Install: skillInstallSpecForTest(),
	}, "repo", "all", false)
	if err == nil {
		t.Fatalf("expected existing destination error")
	}
	if !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractZipArchiveRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("../evil.txt")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("new zip reader: %v", err)
	}
	if _, err := extractZipArchive(reader, t.TempDir()); err == nil {
		t.Fatalf("expected traversal rejection")
	}
}

func skillInstallSpecForTest() skillmeta.InstallSpec {
	return skillmeta.InstallSpec{
		Repo:   "openai/skills",
		Path:   "skills/.curated/my-skill",
		Ref:    "main",
		Method: "auto",
	}
}

func writeTestEnvelope(w http.ResponseWriter, status int, ok bool, data any, apiErr any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":         ok,
		"data":       data,
		"error":      apiErr,
		"pagination": nil,
	})
}
