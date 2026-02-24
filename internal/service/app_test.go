package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"opencortex/internal/broker"
	"opencortex/internal/config"
	"opencortex/internal/storage"
	"opencortex/internal/storage/repos"
)

func setupServiceTestApp(t *testing.T) *App {
	t.Helper()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "service-test.db")

	db, err := storage.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	store := repos.New(db)
	if err := store.SeedRBAC(context.Background()); err != nil {
		t.Fatalf("seed rbac: %v", err)
	}
	memBroker := broker.NewMemory(cfg.Broker.ChannelBufferSize)
	return New(cfg, store, memBroker)
}

func TestAutoRegisterLocalAssignsUniqueName(t *testing.T) {
	app := setupServiceTestApp(t)
	ctx := context.Background()

	a1, _, err := app.AutoRegisterLocal(ctx, "", "fp-1")
	if err != nil {
		t.Fatalf("first auto-register: %v", err)
	}
	if a1.Name != "local-agent" {
		t.Fatalf("expected first name local-agent, got %s", a1.Name)
	}

	a2, _, err := app.AutoRegisterLocal(ctx, "", "fp-2")
	if err != nil {
		t.Fatalf("second auto-register: %v", err)
	}
	if a2.Name == a1.Name {
		t.Fatalf("expected unique name, got duplicate %s", a2.Name)
	}
	if !strings.HasPrefix(a2.Name, "local-agent-") {
		t.Fatalf("expected suffixed auto name, got %s", a2.Name)
	}
}

func TestAutoRegisterLocalReusesFingerprintIdentity(t *testing.T) {
	app := setupServiceTestApp(t)
	ctx := context.Background()

	a1, k1, err := app.AutoRegisterLocal(ctx, "agent-a", "same-fp")
	if err != nil {
		t.Fatalf("first auto-register: %v", err)
	}
	if k1 == "" {
		t.Fatal("expected api key")
	}

	a2, k2, err := app.AutoRegisterLocal(ctx, "agent-b", "same-fp")
	if err != nil {
		t.Fatalf("second auto-register: %v", err)
	}
	if a1.ID != a2.ID {
		t.Fatalf("expected same agent id for fingerprint, got %s vs %s", a1.ID, a2.ID)
	}
	if k2 == "" || k1 == k2 {
		t.Fatal("expected rotated api key on re-register")
	}
}

func TestAutoRegisterLocalReactivatesInactiveFingerprintAgent(t *testing.T) {
	app := setupServiceTestApp(t)
	ctx := context.Background()

	a1, _, err := app.AutoRegisterLocal(ctx, "agent-a", "same-fp")
	if err != nil {
		t.Fatalf("first auto-register: %v", err)
	}

	inactive := "inactive"
	if _, err := app.Store.UpdateAgent(ctx, a1.ID, nil, nil, nil, &inactive); err != nil {
		t.Fatalf("set inactive: %v", err)
	}

	a2, _, err := app.AutoRegisterLocal(ctx, "agent-a", "same-fp")
	if err != nil {
		t.Fatalf("second auto-register: %v", err)
	}
	if a2.ID != a1.ID {
		t.Fatalf("expected same agent id, got %s vs %s", a2.ID, a1.ID)
	}
	if a2.Status != "active" {
		t.Fatalf("expected reactivated status active, got %s", a2.Status)
	}
}
