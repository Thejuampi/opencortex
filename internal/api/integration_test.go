package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestSkillsSpecialKnowledgeLifecycle(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "skills.db")
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

	createResp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/skills", adminKey, map[string]any{
		"title":   "OpenAPI Skill",
		"content": "# Skill\n\ncontent",
		"slug":    "openapi-skill",
		"install": map[string]any{
			"repo":   "openai/skills",
			"path":   "skills/.curated/openapi",
			"ref":    "main",
			"method": "auto",
		},
		"tags": []string{"api"},
	})
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create skill status: %d", createResp.StatusCode)
	}
	var createEnv struct {
		OK   bool `json:"ok"`
		Data struct {
			Skill struct {
				ID      string `json:"id"`
				Slug    string `json:"slug"`
				Install struct {
					Repo string `json:"repo"`
					Ref  string `json:"ref"`
				} `json:"install"`
			} `json:"skill"`
		} `json:"data"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createEnv); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if !createEnv.OK || createEnv.Data.Skill.ID == "" {
		t.Fatalf("unexpected create payload: %+v", createEnv)
	}
	if createEnv.Data.Skill.Slug != "openapi-skill" {
		t.Fatalf("unexpected slug: %s", createEnv.Data.Skill.Slug)
	}
	if createEnv.Data.Skill.Install.Repo != "openai/skills" {
		t.Fatalf("unexpected install repo: %s", createEnv.Data.Skill.Install.Repo)
	}
	skillID := createEnv.Data.Skill.ID

	dupResp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/skills", adminKey, map[string]any{
		"title":   "OpenAPI Skill 2",
		"content": "duplicate",
		"slug":    "openapi-skill",
		"install": map[string]any{
			"repo": "openai/skills",
			"path": "skills/.curated/openapi2",
		},
	})
	defer dupResp.Body.Close()
	if dupResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected slug conflict, got %d", dupResp.StatusCode)
	}

	knowledgeResp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/knowledge", adminKey, map[string]any{
		"title":   "plain knowledge",
		"content": "not a skill",
	})
	defer knowledgeResp.Body.Close()
	if knowledgeResp.StatusCode != http.StatusCreated {
		t.Fatalf("create plain knowledge status: %d", knowledgeResp.StatusCode)
	}
	var knowledgeEnv struct {
		Data struct {
			Knowledge struct {
				ID string `json:"id"`
			} `json:"knowledge"`
		} `json:"data"`
	}
	if err := json.NewDecoder(knowledgeResp.Body).Decode(&knowledgeEnv); err != nil {
		t.Fatalf("decode plain knowledge: %v", err)
	}
	plainKnowledgeID := knowledgeEnv.Data.Knowledge.ID

	listResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/skills?limit=50", adminKey, nil)
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list skills status: %d", listResp.StatusCode)
	}
	var listEnv struct {
		Data struct {
			Skills []struct {
				ID string `json:"id"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list skills: %v", err)
	}
	if len(listEnv.Data.Skills) != 1 || listEnv.Data.Skills[0].ID != skillID {
		t.Fatalf("expected exactly one skill in list, got %+v", listEnv.Data.Skills)
	}
	searchResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/skills?q=openapi-skill", adminKey, nil)
	defer searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("search skills status with hyphenated slug: %d", searchResp.StatusCode)
	}
	var searchEnv struct {
		Data struct {
			Skills []struct {
				ID string `json:"id"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.NewDecoder(searchResp.Body).Decode(&searchEnv); err != nil {
		t.Fatalf("decode search skills: %v", err)
	}
	if len(searchEnv.Data.Skills) == 0 || searchEnv.Data.Skills[0].ID != skillID {
		t.Fatalf("expected slug query to return created skill, got %+v", searchEnv.Data.Skills)
	}

	plainGet := doJSON(t, http.MethodGet, ts.URL+"/api/v1/skills/"+plainKnowledgeID, adminKey, nil)
	defer plainGet.Body.Close()
	if plainGet.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for non-skill knowledge entry, got %d", plainGet.StatusCode)
	}

	patchResp := doJSON(t, http.MethodPatch, ts.URL+"/api/v1/skills/"+skillID, adminKey, map[string]any{
		"install": map[string]any{
			"ref": "dev",
		},
		"tags": []string{"api", "review"},
	})
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch skill status: %d", patchResp.StatusCode)
	}
	var patchEnv struct {
		Data struct {
			Skill struct {
				Install struct {
					Ref string `json:"ref"`
				} `json:"install"`
			} `json:"skill"`
		} `json:"data"`
	}
	if err := json.NewDecoder(patchResp.Body).Decode(&patchEnv); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patchEnv.Data.Skill.Install.Ref != "dev" {
		t.Fatalf("expected install.ref=dev, got %s", patchEnv.Data.Skill.Install.Ref)
	}

	historyResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/skills/"+skillID+"/history", adminKey, nil)
	defer historyResp.Body.Close()
	if historyResp.StatusCode != http.StatusOK {
		t.Fatalf("history status: %d", historyResp.StatusCode)
	}
	var historyEnv struct {
		Data struct {
			History []map[string]any `json:"history"`
		} `json:"data"`
	}
	if err := json.NewDecoder(historyResp.Body).Decode(&historyEnv); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(historyEnv.Data.History) == 0 {
		t.Fatalf("expected non-empty history")
	}

	versionResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/skills/"+skillID+"/versions/1", adminKey, nil)
	defer versionResp.Body.Close()
	if versionResp.StatusCode != http.StatusOK {
		t.Fatalf("version status: %d", versionResp.StatusCode)
	}

	pinResp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/skills/"+skillID+"/pin", adminKey, map[string]any{})
	defer pinResp.Body.Close()
	if pinResp.StatusCode != http.StatusOK {
		t.Fatalf("pin status: %d", pinResp.StatusCode)
	}
	var pinEnv struct {
		Data struct {
			Skill struct {
				IsPinned bool `json:"is_pinned"`
			} `json:"skill"`
		} `json:"data"`
	}
	if err := json.NewDecoder(pinResp.Body).Decode(&pinEnv); err != nil {
		t.Fatalf("decode pin: %v", err)
	}
	if !pinEnv.Data.Skill.IsPinned {
		t.Fatalf("expected skill to be pinned")
	}

	unpinResp := doJSON(t, http.MethodDelete, ts.URL+"/api/v1/skills/"+skillID+"/pin", adminKey, nil)
	defer unpinResp.Body.Close()
	if unpinResp.StatusCode != http.StatusOK {
		t.Fatalf("unpin status: %d", unpinResp.StatusCode)
	}
	var unpinEnv struct {
		Data struct {
			Skill struct {
				IsPinned bool `json:"is_pinned"`
			} `json:"skill"`
		} `json:"data"`
	}
	if err := json.NewDecoder(unpinResp.Body).Decode(&unpinEnv); err != nil {
		t.Fatalf("decode unpin: %v", err)
	}
	if unpinEnv.Data.Skill.IsPinned {
		t.Fatalf("expected skill to be unpinned")
	}
}

func TestWebLocalAdminAuthZeroCeremony(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "web-local-auth.db")
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
	_, _, err = app.BootstrapInit(ctx, "admin")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	syncEngine := syncer.NewEngine(db, store)
	handler := handlers.New(app, db, cfg, syncEngine)
	hub := ws.NewHub(app, store)
	router := api.NewRouter(handler, app, hub)
	ts := httptest.NewServer(router)
	defer ts.Close()

	webAuthResp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/web/auth/local-admin", "", map[string]any{})
	defer webAuthResp.Body.Close()
	if webAuthResp.StatusCode != http.StatusOK {
		t.Fatalf("web local auth status: %d", webAuthResp.StatusCode)
	}
	var webAuthEnv struct {
		OK   bool `json:"ok"`
		Data struct {
			APIKey string   `json:"api_key"`
			Roles  []string `json:"roles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(webAuthResp.Body).Decode(&webAuthEnv); err != nil {
		t.Fatalf("decode web auth: %v", err)
	}
	if !webAuthEnv.OK || webAuthEnv.Data.APIKey == "" {
		t.Fatalf("expected api key in web auth response")
	}
	foundAdmin := false
	for _, role := range webAuthEnv.Data.Roles {
		if role == "admin" {
			foundAdmin = true
			break
		}
	}
	if !foundAdmin {
		t.Fatalf("expected admin role in web auth response, got %v", webAuthEnv.Data.Roles)
	}

	statsResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/admin/stats", webAuthEnv.Data.APIKey, nil)
	defer statsResp.Body.Close()
	if statsResp.StatusCode != http.StatusOK {
		t.Fatalf("admin stats with web local key status: %d", statsResp.StatusCode)
	}
}

func TestMessageClaimAckAndRedelivery(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "claim.db")
	cfg.Auth.Enabled = true
	cfg.MCP.Delivery.DefaultLeaseSeconds = 1
	cfg.MCP.Delivery.MaxLeaseSeconds = 2

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
		Name:        "worker",
		Type:        model.AgentTypeAI,
		Description: "claim worker",
		Status:      model.AgentStatusActive,
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

	msgID := publishDirectMessage(t, ts.URL, adminKey, worker.ID, "claim-me")
	claims := claimMessages(t, ts.URL, workerKey, 1)
	if len(claims) != 1 {
		t.Fatalf("expected one claim, got %d", len(claims))
	}
	if claims[0].Message.ID != msgID {
		t.Fatalf("expected claimed id %s, got %s", msgID, claims[0].Message.ID)
	}

	ackPayload := map[string]any{
		"claim_token": claims[0].ClaimToken,
		"mark_read":   true,
	}
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/messages/"+msgID+"/ack", workerKey, ackPayload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack status: %d", resp.StatusCode)
	}

	readResp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/messages?all=true", workerKey, nil)
	defer readResp.Body.Close()
	if readResp.StatusCode != http.StatusOK {
		t.Fatalf("inbox status: %d", readResp.StatusCode)
	}
	var inboxEnv struct {
		OK   bool `json:"ok"`
		Data struct {
			Messages []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"messages"`
		} `json:"data"`
	}
	if err := json.NewDecoder(readResp.Body).Decode(&inboxEnv); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(inboxEnv.Data.Messages) == 0 || inboxEnv.Data.Messages[0].ID != msgID {
		t.Fatalf("expected read message in inbox")
	}

	expiringID := publishDirectMessage(t, ts.URL, adminKey, worker.ID, "expire-claim")
	expiringClaims := claimMessages(t, ts.URL, workerKey, 1)
	if len(expiringClaims) != 1 {
		t.Fatalf("expected claim for expiring message")
	}
	token := expiringClaims[0].ClaimToken
	time.Sleep(1100 * time.Millisecond)

	expiredAck := doJSON(t, http.MethodPost, ts.URL+"/api/v1/messages/"+expiringID+"/ack", workerKey, map[string]any{
		"claim_token": token,
		"mark_read":   true,
	})
	defer expiredAck.Body.Close()
	if expiredAck.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for expired ack, got %d", expiredAck.StatusCode)
	}

	reclaims := claimMessages(t, ts.URL, workerKey, 1)
	if len(reclaims) != 1 || reclaims[0].Message.ID != expiringID {
		t.Fatalf("expected redelivery claim for %s", expiringID)
	}
}

func TestBroadcastMessageDelivery(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "broadcast.db")
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
		Name:        "worker",
		Type:        model.AgentTypeAI,
		Description: "broadcast worker",
		Status:      model.AgentStatusActive,
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

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/messages/broadcast", adminKey, map[string]any{
		"content":      "all agents checkpoint now",
		"priority":     "critical",
		"type":         "system",
		"content_type": "text/plain",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("broadcast status: %d", resp.StatusCode)
	}

	inbox := doJSON(t, http.MethodGet, ts.URL+"/api/v1/messages?limit=10", workerKey, nil)
	defer inbox.Body.Close()
	if inbox.StatusCode != http.StatusOK {
		t.Fatalf("worker inbox status: %d", inbox.StatusCode)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Messages []model.Message `json:"messages"`
		} `json:"data"`
	}
	if err := json.NewDecoder(inbox.Body).Decode(&env); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(env.Data.Messages) == 0 {
		t.Fatalf("expected at least one message in worker inbox")
	}
	if env.Data.Messages[0].TopicID == nil || *env.Data.Messages[0].TopicID != service.SystemBroadcastTopicID {
		t.Fatalf("expected broadcast topic message, got %#v", env.Data.Messages[0].TopicID)
	}
	if env.Data.Messages[0].Content != "all agents checkpoint now" {
		t.Fatalf("unexpected broadcast content: %s", env.Data.Messages[0].Content)
	}
	_ = worker
}

func TestGroupFanoutAndQueueSemantics(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(tmp, "groups.db")
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
	workerA, workerAKey, err := app.CreateAgent(ctx, repos.CreateAgentInput{
		Name:        "worker-a",
		Type:        model.AgentTypeAI,
		Description: "fanout worker a",
		Status:      model.AgentStatusActive,
	}, "live", "agent")
	if err != nil {
		t.Fatalf("create worker-a: %v", err)
	}
	workerB, workerBKey, err := app.CreateAgent(ctx, repos.CreateAgentInput{
		Name:        "worker-b",
		Type:        model.AgentTypeAI,
		Description: "fanout worker b",
		Status:      model.AgentStatusActive,
	}, "live", "agent")
	if err != nil {
		t.Fatalf("create worker-b: %v", err)
	}

	syncEngine := syncer.NewEngine(db, store)
	handler := handlers.New(app, db, cfg, syncEngine)
	hub := ws.NewHub(app, store)
	router := api.NewRouter(handler, app, hub)
	ts := httptest.NewServer(router)
	defer ts.Close()

	fanoutGroupID := createGroup(t, ts.URL, adminKey, "backend-fanout", "fanout")
	addGroupMember(t, ts.URL, adminKey, fanoutGroupID, workerA.ID)
	addGroupMember(t, ts.URL, adminKey, fanoutGroupID, workerB.ID)

	fanoutMessageID := publishGroupMessage(t, ts.URL, adminKey, fanoutGroupID, false, "fanout notice")
	claimsA := claimMessages(t, ts.URL, workerAKey, 5)
	claimsB := claimMessages(t, ts.URL, workerBKey, 5)
	assertClaimIncludes(t, claimsA, fanoutMessageID)
	assertClaimIncludes(t, claimsB, fanoutMessageID)

	queueGroupID := createGroup(t, ts.URL, adminKey, "review-queue", "queue")
	addGroupMember(t, ts.URL, adminKey, queueGroupID, workerA.ID)
	addGroupMember(t, ts.URL, adminKey, queueGroupID, workerB.ID)

	queueMessageID := publishGroupMessage(t, ts.URL, adminKey, queueGroupID, true, "review this patch")
	queueClaimsA := claimMessages(t, ts.URL, workerAKey, 5)
	queueClaimsB := claimMessages(t, ts.URL, workerBKey, 5)

	gotA := containsClaim(queueClaimsA, queueMessageID)
	gotB := containsClaim(queueClaimsB, queueMessageID)
	if gotA == gotB {
		t.Fatalf("expected queue message to be claimed by exactly one worker (A=%v, B=%v)", gotA, gotB)
	}

	time.Sleep(1100 * time.Millisecond)
	var retryClaims []struct {
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		ClaimToken string `json:"claim_token"`
	}
	if gotA {
		retryClaims = claimMessages(t, ts.URL, workerBKey, 5)
	} else {
		retryClaims = claimMessages(t, ts.URL, workerAKey, 5)
	}
	if !containsClaim(retryClaims, queueMessageID) {
		t.Fatalf("expected expired queue claim to be re-claimable by another group member")
	}
}

type claimEnvelope struct {
	OK   bool `json:"ok"`
	Data struct {
		Claims []struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
			ClaimToken string `json:"claim_token"`
		} `json:"claims"`
	} `json:"data"`
}

func claimMessages(t *testing.T, baseURL, apiKey string, limit int) []struct {
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ClaimToken string `json:"claim_token"`
} {
	t.Helper()
	resp := doJSON(t, http.MethodPost, baseURL+"/api/v1/messages/claim", apiKey, map[string]any{
		"limit":         limit,
		"lease_seconds": 1,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim status: %d", resp.StatusCode)
	}
	var env claimEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	return env.Data.Claims
}

func publishDirectMessage(t *testing.T, baseURL, apiKey, toAgentID, content string) string {
	t.Helper()
	resp := doJSON(t, http.MethodPost, baseURL+"/api/v1/messages", apiKey, map[string]any{
		"to_agent_id":        toAgentID,
		"content_type":       "text/plain",
		"content":            content,
		"priority":           "high",
		"metadata":           map[string]any{"reply_topic": "tasks.result", "type": "task"},
		"tags":               []string{"test"},
		"expires_in_seconds": 3600,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status: %d", resp.StatusCode)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode publish: %v", err)
	}
	return env.Data.Message.ID
}

func doJSON(t *testing.T, method, url, apiKey string, payload any) *http.Response {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func createGroup(t *testing.T, baseURL, apiKey, name, mode string) string {
	t.Helper()
	resp := doJSON(t, http.MethodPost, baseURL+"/api/v1/groups", apiKey, map[string]any{
		"name":        name,
		"description": name + " group",
		"mode":        mode,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group status: %d", resp.StatusCode)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Group struct {
				ID string `json:"id"`
			} `json:"group"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode create group: %v", err)
	}
	return env.Data.Group.ID
}

func addGroupMember(t *testing.T, baseURL, apiKey, groupID, agentID string) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, baseURL+"/api/v1/groups/"+groupID+"/members", apiKey, map[string]any{
		"agent_id": agentID,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add group member status: %d", resp.StatusCode)
	}
}

func publishGroupMessage(t *testing.T, baseURL, apiKey, groupID string, queueMode bool, content string) string {
	t.Helper()
	resp := doJSON(t, http.MethodPost, baseURL+"/api/v1/messages", apiKey, map[string]any{
		"to_group_id":  groupID,
		"queue_mode":   queueMode,
		"content":      content,
		"content_type": "text/plain",
		"priority":     "normal",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish group message status: %d", resp.StatusCode)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode publish group message: %v", err)
	}
	return env.Data.Message.ID
}

func assertClaimIncludes(t *testing.T, claims []struct {
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ClaimToken string `json:"claim_token"`
}, messageID string) {
	t.Helper()
	if !containsClaim(claims, messageID) {
		t.Fatalf("expected claims to include message %s", messageID)
	}
}

func containsClaim(claims []struct {
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ClaimToken string `json:"claim_token"`
}, messageID string) bool {
	for _, c := range claims {
		if c.Message.ID == messageID {
			return true
		}
	}
	return false
}
