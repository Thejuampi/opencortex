package repos

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
