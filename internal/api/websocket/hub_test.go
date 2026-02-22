package websocket_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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

func TestHubDirectMailboxDelivery(t *testing.T) {
	env := setupHubTestEnv(t)
	defer env.cleanup()

	conn := env.connectWS(t)
	defer conn.Close()

	_ = readFrame(t, conn) // connected ack

	msg, err := env.app.CreateMessage(context.Background(), repos.CreateMessageInput{
		FromAgentID: env.admin.ID,
		ToAgentID:   &env.worker.ID,
		ContentType: "text/plain",
		Content:     "direct-task",
		Priority:    model.MessagePriorityHigh,
	})
	if err != nil {
		t.Fatalf("create direct message: %v", err)
	}

	hint := readType(t, conn, "delta")
	if got := nestedString(hint, "data", "id"); got != msg.ID {
		t.Fatalf("unexpected hint id: %s", got)
	}

	payload := readType(t, conn, "message")
	if got := nestedString(payload, "data", "id"); got != msg.ID {
		t.Fatalf("unexpected message id: %s", got)
	}
}

func TestHubTopicAndDirectDelivery(t *testing.T) {
	env := setupHubTestEnv(t)
	defer env.cleanup()

	topic, err := env.app.CreateTopic(context.Background(), repos.CreateTopicInput{
		ID:          "",
		Name:        "task-flow",
		Description: "tests",
		CreatedBy:   env.admin.ID,
		IsPublic:    true,
	})
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}

	conn := env.connectWS(t)
	defer conn.Close()
	_ = readFrame(t, conn) // connected ack

	if err := conn.WriteJSON(map[string]any{"type": "subscribe", "topic_id": topic.ID}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = readType(t, conn, "ack")

	topicMsg, err := env.app.CreateMessage(context.Background(), repos.CreateMessageInput{
		FromAgentID: env.admin.ID,
		TopicID:     &topic.ID,
		ContentType: "text/plain",
		Content:     "topic-work",
		Priority:    model.MessagePriorityNormal,
	})
	if err != nil {
		t.Fatalf("create topic message: %v", err)
	}

	directMsg, err := env.app.CreateMessage(context.Background(), repos.CreateMessageInput{
		FromAgentID: env.admin.ID,
		ToAgentID:   &env.worker.ID,
		ContentType: "text/plain",
		Content:     "direct-work",
		Priority:    model.MessagePriorityHigh,
	})
	if err != nil {
		t.Fatalf("create direct message: %v", err)
	}

	seen := map[string]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		frame := readFrame(t, conn)
		if frameType, _ := frame["type"].(string); frameType == "message" {
			id := nestedString(frame, "data", "id")
			if id == topicMsg.ID || id == directMsg.ID {
				seen[id] = true
			}
		}
	}

	if !seen[topicMsg.ID] || !seen[directMsg.ID] {
		t.Fatalf("expected both topic and direct message delivery, got %#v", seen)
	}
}

type hubTestEnv struct {
	app       *service.App
	admin     model.Agent
	worker    model.Agent
	workerKey string
	wsURL     string
	cleanup   func()
}

func setupHubTestEnv(t *testing.T) hubTestEnv {
	t.Helper()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "hub-test.db")

	ctx := context.Background()
	db, err := storage.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.Migrate(ctx, db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}

	store := repos.New(db)
	mem := broker.NewMemory(64)
	app := service.New(cfg, store, mem)
	admin, _, err := app.BootstrapInit(ctx, "admin")
	if err != nil {
		_ = db.Close()
		t.Fatalf("bootstrap: %v", err)
	}

	worker, workerKey, err := app.CreateAgent(ctx, repos.CreateAgentInput{
		Name:        "worker",
		Type:        model.AgentTypeAI,
		Description: "ws worker",
		Status:      model.AgentStatusActive,
		Tags:        []string{"test"},
	}, "live", "agent")
	if err != nil {
		_ = db.Close()
		t.Fatalf("create agent: %v", err)
	}

	syncEngine := syncer.NewEngine(db, store)
	h := ws.NewHub(app, store)
	h.Start(ctx)
	server := handlers.New(app, db, cfg, syncEngine)
	router := api.NewRouter(server, app, h)
	ts := httptest.NewServer(router)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws?api_key=" + url.QueryEscape(workerKey)

	cleanup := func() {
		ts.Close()
		_ = db.Close()
	}

	return hubTestEnv{
		app:       app,
		admin:     admin,
		worker:    worker,
		workerKey: workerKey,
		wsURL:     wsURL,
		cleanup:   cleanup,
	}
}

func (e hubTestEnv) connectWS(t *testing.T) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(e.wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return conn
}

func readType(t *testing.T, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frame := readFrame(t, conn)
		if got, _ := frame["type"].(string); got == want {
			return frame
		}
	}
	t.Fatalf("did not receive frame type %s", want)
	return nil
}

func readFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	return frame
}

func nestedString(m map[string]any, path ...string) string {
	cur := any(m)
	for _, part := range path {
		node, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = node[part]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}
