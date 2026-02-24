package repos

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencortex/internal/config"
	"opencortex/internal/model"
	"opencortex/internal/storage"
)

func TestCreateAgentRejectsDuplicateNameCaseInsensitive(t *testing.T) {
	ctx := context.Background()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "agents-test.db")

	db, err := storage.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	store := New(db)
	_, err = store.CreateAgent(ctx, CreateAgentInput{
		ID:          newID(),
		Name:        "Alpha",
		Type:        model.AgentTypeAI,
		APIKeyHash:  "hash-one",
		Description: "first",
		Status:      model.AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}

	_, err = store.CreateAgent(ctx, CreateAgentInput{
		ID:          newID(),
		Name:        "alpha",
		Type:        model.AgentTypeAI,
		APIKeyHash:  "hash-two",
		Description: "second",
		Status:      model.AgentStatusActive,
	})
	if err == nil {
		t.Fatal("expected duplicate name constraint error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("expected unique constraint error, got: %v", err)
	}
}

func TestDeleteInactiveAutoAgentsCascade(t *testing.T) {
	ctx := context.Background()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "agents-expire-test.db")

	db, err := storage.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	store := New(db)
	if err := store.SeedRBAC(ctx); err != nil {
		t.Fatalf("seed rbac: %v", err)
	}

	auto, err := store.CreateAgent(ctx, CreateAgentInput{
		ID:         newID(),
		Name:       "auto-old",
		Type:       model.AgentTypeAI,
		APIKeyHash: "hash-auto",
		Tags:       []string{"auto"},
		Status:     model.AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("create auto: %v", err)
	}
	admin, err := store.CreateAgent(ctx, CreateAgentInput{
		ID:         newID(),
		Name:       "admin-old",
		Type:       model.AgentTypeHuman,
		APIKeyHash: "hash-admin",
		Tags:       []string{"auto", "admin"},
		Status:     model.AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := store.AssignRole(ctx, admin.ID, "admin"); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}

	old := nowUTC().Add(-48 * time.Hour).Format(timeFormat)
	if _, err := db.ExecContext(ctx, "UPDATE agents SET created_at = ?, last_seen = ? WHERE id IN (?, ?)", old, old, auto.ID, admin.ID); err != nil {
		t.Fatalf("backdate agents: %v", err)
	}

	affected, err := store.DeleteInactiveAutoAgentsCascade(ctx, nowUTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("delete inactive auto agents: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 deleted agent, got %d", affected)
	}

	if _, err := store.GetAgentByID(ctx, auto.ID); err == nil {
		t.Fatalf("expected auto agent to be deleted")
	}
	gotAdmin, err := store.GetAgentByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if gotAdmin.Status != model.AgentStatusActive {
		t.Fatalf("expected admin to stay active, got %s", gotAdmin.Status)
	}
}

func TestDeleteInactiveAutoAgentsCascadeRewritesKnowledgeRefs(t *testing.T) {
	ctx := context.Background()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "agents-expire-knowledge-test.db")

	db, err := storage.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	store := New(db)
	if err := store.SeedRBAC(ctx); err != nil {
		t.Fatalf("seed rbac: %v", err)
	}

	admin, err := store.CreateAgent(ctx, CreateAgentInput{
		ID:         newID(),
		Name:       "admin-knowledge",
		Type:       model.AgentTypeHuman,
		APIKeyHash: "hash-admin-knowledge",
		Tags:       []string{"admin"},
		Status:     model.AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := store.AssignRole(ctx, admin.ID, "admin"); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}

	auto, err := store.CreateAgent(ctx, CreateAgentInput{
		ID:         newID(),
		Name:       "auto-knowledge",
		Type:       model.AgentTypeAI,
		APIKeyHash: "hash-auto-knowledge",
		Tags:       []string{"auto"},
		Status:     model.AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("create auto: %v", err)
	}

	now := nowUTC().Format(timeFormat)
	if _, err := db.ExecContext(ctx, `
INSERT INTO knowledge_entries(
  id, title, content, content_type, summary, tags, collection_id, created_by, updated_by, version,
  checksum, is_pinned, visibility, source, metadata, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"k-rewrite-1",
		"Rewrite refs",
		"content",
		"text/markdown",
		"",
		toJSON([]string{"test"}),
		nil,
		admin.ID,
		auto.ID,
		1,
		checksum("content"),
		0,
		"public",
		nil,
		toJSON(map[string]any{}),
		now,
		now,
	); err != nil {
		t.Fatalf("insert knowledge entry: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO knowledge_versions(id, knowledge_id, version, content, summary, changed_by, change_note, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"kv-rewrite-1",
		"k-rewrite-1",
		1,
		"content",
		"",
		auto.ID,
		"note",
		now,
	); err != nil {
		t.Fatalf("insert knowledge version: %v", err)
	}

	old := nowUTC().Add(-48 * time.Hour).Format(timeFormat)
	if _, err := db.ExecContext(ctx, "UPDATE agents SET created_at = ?, last_seen = ? WHERE id = ?", old, old, auto.ID); err != nil {
		t.Fatalf("backdate auto agent: %v", err)
	}

	affected, err := store.DeleteInactiveAutoAgentsCascade(ctx, nowUTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("delete inactive auto agents: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 deleted agent, got %d", affected)
	}

	entry, err := store.GetKnowledge(ctx, "k-rewrite-1")
	if err != nil {
		t.Fatalf("get knowledge entry: %v", err)
	}
	if entry.UpdatedBy != admin.ID {
		t.Fatalf("expected updated_by rewritten to creator %s, got %s", admin.ID, entry.UpdatedBy)
	}

	version, err := store.KnowledgeVersion(ctx, "k-rewrite-1", 1)
	if err != nil {
		t.Fatalf("get knowledge version: %v", err)
	}
	if version.ChangedBy != admin.ID {
		t.Fatalf("expected changed_by rewritten to creator %s, got %s", admin.ID, version.ChangedBy)
	}
}
