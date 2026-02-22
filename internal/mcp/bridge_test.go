package mcpbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcptypes "github.com/mark3labs/mcp-go/mcp"

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

func TestMCPInProcessToolRoundTrip(t *testing.T) {
	env := setupMCPTestEnv(t)
	defer env.cleanup()

	bridge := New(Options{
		App:           env.app,
		Config:        env.cfg,
		Router:        env.router,
		DefaultAPIKey: env.adminKey,
	})

	client, err := mcpclient.NewInProcessClient(bridge.MCPServer())
	if err != nil {
		t.Fatalf("new in-process client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start client: %v", err)
	}
	if _, err := client.Initialize(ctx, initializeRequest()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	tools, err := client.ListTools(ctx, mcptypes.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if !hasTool(tools.Tools, "messages_broadcast") || !hasTool(tools.Tools, "groups_list") {
		t.Fatalf("expected broadcast and groups tools to be exposed")
	}

	result, err := client.CallTool(ctx, mcptypes.CallToolRequest{
		Params: mcptypes.CallToolParams{Name: "agents_me"},
	})
	if err != nil {
		t.Fatalf("call agents_me: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %#v", result)
	}

	publish, err := client.CallTool(ctx, mcptypes.CallToolRequest{
		Params: mcptypes.CallToolParams{
			Name: "messages_publish",
			Arguments: map[string]any{
				"payload": map[string]any{
					"to_agent_id":  env.worker.ID,
					"content":      "mcp publish",
					"content_type": "text/plain",
					"priority":     "high",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("call messages_publish: %v", err)
	}
	if publish.IsError {
		t.Fatalf("publish returned error: %#v", publish)
	}
}

func TestMCPHTTPAuthAndRBAC(t *testing.T) {
	env := setupMCPTestEnv(t)
	defer env.cleanup()

	bridge := New(Options{
		App:    env.app,
		Config: env.cfg,
		Router: env.router,
	})

	ts := httptest.NewServer(bridge.HTTPHandler())
	defer ts.Close()

	ctx := context.Background()

	adminClient, err := mcpclient.NewStreamableHttpClient(
		ts.URL+env.cfg.MCP.HTTP.Path,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + env.adminKey}),
	)
	if err != nil {
		t.Fatalf("new admin http client: %v", err)
	}
	defer adminClient.Close()

	if err := adminClient.Start(ctx); err != nil {
		t.Fatalf("start admin client: %v", err)
	}
	if _, err := adminClient.Initialize(ctx, initializeRequest()); err != nil {
		t.Fatalf("init admin client: %v", err)
	}

	okResult, err := adminClient.CallTool(ctx, mcptypes.CallToolRequest{
		Params: mcptypes.CallToolParams{Name: "admin_stats"},
	})
	if err != nil {
		t.Fatalf("admin_stats call: %v", err)
	}
	if okResult.IsError {
		t.Fatalf("expected admin_stats success, got error result")
	}

	workerClient, err := mcpclient.NewStreamableHttpClient(
		ts.URL+env.cfg.MCP.HTTP.Path,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + env.workerKey}),
	)
	if err != nil {
		t.Fatalf("new worker http client: %v", err)
	}
	defer workerClient.Close()

	if err := workerClient.Start(ctx); err != nil {
		t.Fatalf("start worker client: %v", err)
	}
	if _, err := workerClient.Initialize(ctx, initializeRequest()); err != nil {
		t.Fatalf("init worker client: %v", err)
	}

	denied, err := workerClient.CallTool(ctx, mcptypes.CallToolRequest{
		Params: mcptypes.CallToolParams{Name: "admin_stats"},
	})
	if err != nil {
		t.Fatalf("worker admin_stats call: %v", err)
	}
	if !denied.IsError {
		t.Fatalf("expected RBAC denial for worker on admin_stats")
	}
}

type mcpTestEnv struct {
	cfg       config.Config
	app       *service.App
	router    http.Handler
	worker    model.Agent
	adminKey  string
	workerKey string
	cleanup   func()
}

func setupMCPTestEnv(t *testing.T) mcpTestEnv {
	t.Helper()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "mcp-test.db")
	cfg.Auth.Enabled = true
	cfg.MCP.Enabled = true
	cfg.MCP.HTTP.Enabled = true
	cfg.MCP.HTTP.Path = "/mcp"

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
	_, adminKey, err := app.BootstrapInit(ctx, "admin")
	if err != nil {
		_ = db.Close()
		t.Fatalf("bootstrap: %v", err)
	}

	worker, workerKey, err := app.CreateAgent(ctx, repos.CreateAgentInput{
		Name:        "worker",
		Type:        model.AgentTypeAI,
		Description: "mcp worker",
		Status:      model.AgentStatusActive,
	}, "live", "agent")
	if err != nil {
		_ = db.Close()
		t.Fatalf("create worker: %v", err)
	}

	syncEngine := syncer.NewEngine(db, store)
	handler := handlers.New(app, db, cfg, syncEngine)
	hub := ws.NewHub(app, store)
	router := api.NewRouter(handler, app, hub)

	return mcpTestEnv{
		cfg:       cfg,
		app:       app,
		router:    router,
		worker:    worker,
		adminKey:  adminKey,
		workerKey: workerKey,
		cleanup: func() {
			_ = db.Close()
		},
	}
}

func initializeRequest() mcptypes.InitializeRequest {
	return mcptypes.InitializeRequest{
		Params: mcptypes.InitializeParams{
			ProtocolVersion: mcptypes.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcptypes.Implementation{
				Name:    "opencortex-test-client",
				Version: "0.0.1",
			},
			Capabilities: mcptypes.ClientCapabilities{},
		},
	}
}

func hasTool(tools []mcptypes.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
