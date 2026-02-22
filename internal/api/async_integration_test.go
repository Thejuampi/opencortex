package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencortex/internal/api"
	"opencortex/internal/api/handlers"
	ws "opencortex/internal/api/websocket"
	"opencortex/internal/broker"
	"opencortex/internal/config"
	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage"
	"opencortex/internal/storage/repos"
	syncer "opencortex/internal/sync"
)

func TestAsyncMailboxAndSweep(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "async.db")
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
	worker, workerKey, err := app.CreateAgent(ctx, repos.CreateAgentInput{
		Name:   "worker",
		Type:   model.AgentTypeAI,
		Status: model.AgentStatusActive,
	}, "live", "agent")
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	syncEngine := syncer.NewEngine(db, store)
	handler := handlers.New(app, db, cfg, syncEngine)
	hub := ws.NewHub(app, store)
	router := api.NewRouter(handler, app, hub)
	ts := httptest.NewServer(router)
	defer ts.Close()

	// 1. Publish a direct message to worker
	msgID1 := publishDirectMessage(t, ts.URL, adminKey, worker.ID, "msg1")

	// 2. Worker reads inbox (peek=false so it marks delivered, lease_seconds=1 for test)
	inboxResp1 := doJSON(t, http.MethodGet, ts.URL+"/api/v1/messages/inbox?lease_seconds=1", workerKey, nil)
	defer inboxResp1.Body.Close()
	var env1 struct {
		Data struct {
			Messages []model.Message `json:"messages"`
			Cursor   string          `json:"cursor"`
		} `json:"data"`
	}
	if err := json.NewDecoder(inboxResp1.Body).Decode(&env1); err != nil {
		t.Fatalf("decode inbox 1: %v", err)
	}
	if len(env1.Data.Messages) != 1 || env1.Data.Messages[0].ID != msgID1 {
		t.Fatalf("expected msg1 in inbox, got %v", env1.Data.Messages)
	}
	cursor1 := env1.Data.Cursor

	// 3. Worker reads inbox again with cursor -> should be empty (already read, cursor passed it? wait, cursor is BEFORE it if we passed empty, but we didn't ack it)
	// Inbox returns un-acked stuff? Yes, GetInbox returns 'pending' or 'delivered'.
	// Wait, since we just got it, it's 'delivered' and leased. Oh! actually GetInboxAsync checks for 'pending' or 'delivered'.
	// Wait, if it's 'delivered', it does NOT re-return it unless included?
	// Let's check `GetInboxFilters`. It returns `mr.status IN ('pending', 'delivered')`.
	// It DOES return delivered messages again if cursor is before them!
	// Wait, if it returns delivered messages, it's fine. It's meant to be idempotent until ack.

	// 4. Sleep for lease expiration (1s)
	time.Sleep(1200 * time.Millisecond)

	// 5. Sweep deliveries
	redelivered, deadLettered, err := store.SweepDeliveries(ctx)
	if err != nil {
		t.Fatalf("sweep error: %v", err)
	}
	if redelivered != 1 || deadLettered != 0 {
		t.Fatalf("expected 1 redelivered, got %d redelivered, %d dead", redelivered, deadLettered)
	}

	// 6. Ack message
	ackResp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/messages/ack", workerKey, map[string]any{
		"ids":   []string{msgID1},
		"up_to": msgID1,
	})
	defer ackResp.Body.Close()
	if ackResp.StatusCode != http.StatusOK {
		t.Fatalf("ack status: %d", ackResp.StatusCode)
	}

	// 7. Read inbox with cursor -> should be empty
	inboxResp2 := doJSON(t, http.MethodGet, ts.URL+"/api/v1/messages/inbox?cursor="+cursor1, workerKey, nil)
	defer inboxResp2.Body.Close()
	var env2 struct {
		Data struct {
			Messages []model.Message `json:"messages"`
			Cursor   string          `json:"cursor"`
		} `json:"data"`
	}
	if err := json.NewDecoder(inboxResp2.Body).Decode(&env2); err != nil {
		t.Fatalf("decode inbox 2: %v", err)
	}
	if len(env2.Data.Messages) != 0 {
		t.Fatalf("expected empty inbox after ack, got %d messages", len(env2.Data.Messages))
	}

	// 8. Inbox --wait behavior
	// Start a goroutine that sends a message after 1 second
	go func() {
		time.Sleep(500 * time.Millisecond)
		publishDirectMessage(t, ts.URL, adminKey, worker.ID, "delayed-msg")
	}()

	startWait := time.Now()
	// Polling should block for up to ~1 sec and return the newly arrived message
	waitResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/messages/inbox?wait=3", workerKey, nil)
	defer waitResp.Body.Close()
	duration := time.Since(startWait)
	if duration < 400*time.Millisecond || duration > 2*time.Second {
		t.Fatalf("wait duration unexpected: %v", duration)
	}

	var envWait struct {
		Data struct {
			Messages []model.Message `json:"messages"`
		} `json:"data"`
	}
	if err := json.NewDecoder(waitResp.Body).Decode(&envWait); err != nil {
		t.Fatalf("decode wait inbox: %v", err)
	}
	if len(envWait.Data.Messages) != 1 || !strings.Contains(envWait.Data.Messages[0].Content, "delayed-msg") {
		t.Fatalf("expected delayed message, got %v", envWait.Data.Messages)
	}
}
