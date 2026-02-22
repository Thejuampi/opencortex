package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"

	mcptypes "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"opencortex/internal/config"
	"opencortex/internal/service"
)

type Options struct {
	App            *service.App
	Config         config.Config
	Router         http.Handler
	DefaultAPIKey  string
	DefaultAgentID string
}

type Bridge struct {
	app            *service.App
	cfg            config.Config
	router         http.Handler
	defaultAPIKey  string
	defaultAgentID string
	server         *mcpserver.MCPServer
}

type ToolSpec struct {
	Name         string
	Description  string
	Method       string
	Path         string
	Resource     string
	Action       string
	HasPayload   bool
	HasQuery     bool
	ExposeAlways bool
}

type apiEnvelope struct {
	OK         bool `json:"ok"`
	Data       any  `json:"data"`
	Error      any  `json:"error"`
	Pagination any  `json:"pagination"`
}

var routeParamPattern = regexp.MustCompile(`\{([^{}]+)\}`)

func New(opts Options) *Bridge {
	b := &Bridge{
		app:            opts.App,
		cfg:            opts.Config,
		router:         opts.Router,
		defaultAPIKey:  strings.TrimSpace(opts.DefaultAPIKey),
		defaultAgentID: strings.TrimSpace(opts.DefaultAgentID),
	}
	b.server = mcpserver.NewMCPServer(
		"opencortex",
		"dev",
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithInstructions("Use Opencortex tools for multi-agent messaging, knowledge, sync, and admin operations."),
	)
	b.registerTools()
	return b
}

func (b *Bridge) MCPServer() *mcpserver.MCPServer {
	return b.server
}

func (b *Bridge) ServeStdio() error {
	return mcpserver.ServeStdio(b.server)
}

func (b *Bridge) HTTPHandler() http.Handler {
	return mcpserver.NewStreamableHTTPServer(
		b.server,
		mcpserver.WithEndpointPath(b.cfg.MCP.HTTP.Path),
	)
}

func (b *Bridge) registerTools() {
	specs := b.toolSpecs()
	for _, spec := range specs {
		if strings.HasPrefix(spec.Name, "admin_") && !b.cfg.MCP.Tools.ExposeAdmin && !spec.ExposeAlways {
			continue
		}
		b.server.AddTool(spec.toTool(), b.makeToolHandler(spec))
	}
}

func (b *Bridge) toolSpecs() []ToolSpec {
	// Full REST parity, grouped by resource.
	return []ToolSpec{
		// Agents
		{Name: "agents_create", Description: "Create an agent", Method: http.MethodPost, Path: "/api/v1/agents", Resource: "agents", Action: "write", HasPayload: true},
		{Name: "agents_list", Description: "List agents", Method: http.MethodGet, Path: "/api/v1/agents", Resource: "agents", Action: "read", HasQuery: true},
		{Name: "agents_me", Description: "Get current authenticated agent", Method: http.MethodGet, Path: "/api/v1/agents/me", Resource: "agents", Action: "read"},
		{Name: "agents_get", Description: "Get an agent by id", Method: http.MethodGet, Path: "/api/v1/agents/{id}", Resource: "agents", Action: "read"},
		{Name: "agents_update", Description: "Update an agent", Method: http.MethodPatch, Path: "/api/v1/agents/{id}", Resource: "agents", Action: "write", HasPayload: true},
		{Name: "agents_delete", Description: "Deactivate an agent", Method: http.MethodDelete, Path: "/api/v1/agents/{id}", Resource: "agents", Action: "manage"},
		{Name: "agents_rotate_key", Description: "Rotate an agent API key", Method: http.MethodPost, Path: "/api/v1/agents/{id}/rotate-key", Resource: "agents", Action: "manage", HasPayload: true},
		{Name: "agents_messages", Description: "List agent messages", Method: http.MethodGet, Path: "/api/v1/agents/{id}/messages", Resource: "messages", Action: "read", HasQuery: true},
		{Name: "agents_topics", Description: "List topics an agent is subscribed to", Method: http.MethodGet, Path: "/api/v1/agents/{id}/topics", Resource: "topics", Action: "read"},

		// Topics
		{Name: "topics_create", Description: "Create a topic", Method: http.MethodPost, Path: "/api/v1/topics", Resource: "topics", Action: "write", HasPayload: true},
		{Name: "topics_list", Description: "List topics", Method: http.MethodGet, Path: "/api/v1/topics", Resource: "topics", Action: "read", HasQuery: true},
		{Name: "topics_get", Description: "Get a topic", Method: http.MethodGet, Path: "/api/v1/topics/{id}", Resource: "topics", Action: "read"},
		{Name: "topics_update", Description: "Update a topic", Method: http.MethodPatch, Path: "/api/v1/topics/{id}", Resource: "topics", Action: "write", HasPayload: true},
		{Name: "topics_delete", Description: "Delete a topic", Method: http.MethodDelete, Path: "/api/v1/topics/{id}", Resource: "topics", Action: "manage"},
		{Name: "topics_subscribers", Description: "List topic subscribers", Method: http.MethodGet, Path: "/api/v1/topics/{id}/subscribers", Resource: "topics", Action: "read"},
		{Name: "topics_messages", Description: "List topic messages", Method: http.MethodGet, Path: "/api/v1/topics/{id}/messages", Resource: "messages", Action: "read", HasQuery: true},
		{Name: "topics_subscribe", Description: "Subscribe current agent to topic", Method: http.MethodPost, Path: "/api/v1/topics/{id}/subscribe", Resource: "topics", Action: "write", HasPayload: true},
		{Name: "topics_unsubscribe", Description: "Unsubscribe current agent from topic", Method: http.MethodDelete, Path: "/api/v1/topics/{id}/subscribe", Resource: "topics", Action: "write"},
		{Name: "topics_members_add", Description: "Add topic member", Method: http.MethodPost, Path: "/api/v1/topics/{id}/members", Resource: "topics", Action: "manage", HasPayload: true},
		{Name: "topics_members_remove", Description: "Remove topic member", Method: http.MethodDelete, Path: "/api/v1/topics/{id}/members/{agent_id}", Resource: "topics", Action: "manage"},
		{Name: "topics_members_list", Description: "List topic members", Method: http.MethodGet, Path: "/api/v1/topics/{id}/members", Resource: "topics", Action: "read"},

		// Groups
		{Name: "groups_create", Description: "Create a group", Method: http.MethodPost, Path: "/api/v1/groups", Resource: "groups", Action: "write", HasPayload: true},
		{Name: "groups_list", Description: "List groups", Method: http.MethodGet, Path: "/api/v1/groups", Resource: "groups", Action: "read", HasQuery: true},
		{Name: "groups_get", Description: "Get a group", Method: http.MethodGet, Path: "/api/v1/groups/{id}", Resource: "groups", Action: "read"},
		{Name: "groups_update", Description: "Update a group", Method: http.MethodPatch, Path: "/api/v1/groups/{id}", Resource: "groups", Action: "write", HasPayload: true},
		{Name: "groups_delete", Description: "Delete a group", Method: http.MethodDelete, Path: "/api/v1/groups/{id}", Resource: "groups", Action: "manage"},
		{Name: "groups_members_add", Description: "Add group member", Method: http.MethodPost, Path: "/api/v1/groups/{id}/members", Resource: "groups", Action: "write", HasPayload: true},
		{Name: "groups_members_remove", Description: "Remove group member", Method: http.MethodDelete, Path: "/api/v1/groups/{id}/members/{agent_id}", Resource: "groups", Action: "manage"},
		{Name: "groups_members_list", Description: "List group members", Method: http.MethodGet, Path: "/api/v1/groups/{id}/members", Resource: "groups", Action: "read"},

		// Messages
		{Name: "messages_publish", Description: "Publish a message to topic or direct recipient", Method: http.MethodPost, Path: "/api/v1/messages", Resource: "messages", Action: "write", HasPayload: true},
		{Name: "messages_broadcast", Description: "Broadcast a message to all agents", Method: http.MethodPost, Path: "/api/v1/messages/broadcast", Resource: "messages", Action: "write", HasPayload: true},
		{Name: "messages_inbox", Description: "Read current agent inbox", Method: http.MethodGet, Path: "/api/v1/messages", Resource: "messages", Action: "read", HasQuery: true},
		{Name: "messages_get", Description: "Get message by id", Method: http.MethodGet, Path: "/api/v1/messages/{id}", Resource: "messages", Action: "read"},
		{Name: "messages_claim", Description: "Claim pending messages with a lease", Method: http.MethodPost, Path: "/api/v1/messages/claim", Resource: "messages", Action: "read", HasPayload: true},
		{Name: "messages_ack", Description: "Acknowledge a claimed message", Method: http.MethodPost, Path: "/api/v1/messages/{id}/ack", Resource: "messages", Action: "write", HasPayload: true},
		{Name: "messages_nack", Description: "Negative-acknowledge a claimed message", Method: http.MethodPost, Path: "/api/v1/messages/{id}/nack", Resource: "messages", Action: "write", HasPayload: true},
		{Name: "messages_renew", Description: "Renew a claim lease", Method: http.MethodPost, Path: "/api/v1/messages/{id}/renew", Resource: "messages", Action: "write", HasPayload: true},
		{Name: "messages_read", Description: "Mark a message as read", Method: http.MethodPost, Path: "/api/v1/messages/{id}/read", Resource: "messages", Action: "write", HasPayload: true},
		{Name: "messages_thread", Description: "Get a message thread", Method: http.MethodGet, Path: "/api/v1/messages/{id}/thread", Resource: "messages", Action: "read"},
		{Name: "messages_delete", Description: "Delete a message", Method: http.MethodDelete, Path: "/api/v1/messages/{id}", Resource: "messages", Action: "write"},

		// Knowledge
		{Name: "knowledge_create", Description: "Create knowledge entry", Method: http.MethodPost, Path: "/api/v1/knowledge", Resource: "knowledge", Action: "write", HasPayload: true},
		{Name: "knowledge_list", Description: "Search/list knowledge", Method: http.MethodGet, Path: "/api/v1/knowledge", Resource: "knowledge", Action: "read", HasQuery: true},
		{Name: "knowledge_get", Description: "Get knowledge entry", Method: http.MethodGet, Path: "/api/v1/knowledge/{id}", Resource: "knowledge", Action: "read"},
		{Name: "knowledge_replace", Description: "Replace knowledge content", Method: http.MethodPut, Path: "/api/v1/knowledge/{id}", Resource: "knowledge", Action: "write", HasPayload: true},
		{Name: "knowledge_patch", Description: "Patch knowledge metadata", Method: http.MethodPatch, Path: "/api/v1/knowledge/{id}", Resource: "knowledge", Action: "write", HasPayload: true},
		{Name: "knowledge_delete", Description: "Delete knowledge entry", Method: http.MethodDelete, Path: "/api/v1/knowledge/{id}", Resource: "knowledge", Action: "manage"},
		{Name: "knowledge_history", Description: "Get knowledge history", Method: http.MethodGet, Path: "/api/v1/knowledge/{id}/history", Resource: "knowledge", Action: "read"},
		{Name: "knowledge_version", Description: "Get specific knowledge version", Method: http.MethodGet, Path: "/api/v1/knowledge/{id}/versions/{v}", Resource: "knowledge", Action: "read"},
		{Name: "knowledge_restore", Description: "Restore knowledge version", Method: http.MethodPost, Path: "/api/v1/knowledge/{id}/restore/{v}", Resource: "knowledge", Action: "write", HasPayload: true},
		{Name: "knowledge_pin", Description: "Pin knowledge entry", Method: http.MethodPost, Path: "/api/v1/knowledge/{id}/pin", Resource: "knowledge", Action: "write", HasPayload: true},
		{Name: "knowledge_unpin", Description: "Unpin knowledge entry", Method: http.MethodDelete, Path: "/api/v1/knowledge/{id}/pin", Resource: "knowledge", Action: "write"},

		// Collections
		{Name: "collections_create", Description: "Create collection", Method: http.MethodPost, Path: "/api/v1/collections", Resource: "collections", Action: "write", HasPayload: true},
		{Name: "collections_list", Description: "List collections", Method: http.MethodGet, Path: "/api/v1/collections", Resource: "collections", Action: "read", HasQuery: true},
		{Name: "collections_get", Description: "Get collection", Method: http.MethodGet, Path: "/api/v1/collections/{id}", Resource: "collections", Action: "read"},
		{Name: "collections_update", Description: "Update collection", Method: http.MethodPatch, Path: "/api/v1/collections/{id}", Resource: "collections", Action: "write", HasPayload: true},
		{Name: "collections_delete", Description: "Delete collection", Method: http.MethodDelete, Path: "/api/v1/collections/{id}", Resource: "collections", Action: "manage"},
		{Name: "collections_knowledge", Description: "List collection knowledge", Method: http.MethodGet, Path: "/api/v1/collections/{id}/knowledge", Resource: "knowledge", Action: "read", HasQuery: true},
		{Name: "collections_tree", Description: "Get collection tree", Method: http.MethodGet, Path: "/api/v1/collections/tree", Resource: "collections", Action: "read"},

		// Sync
		{Name: "sync_remotes_list", Description: "List sync remotes", Method: http.MethodGet, Path: "/api/v1/sync/remotes", Resource: "sync", Action: "read"},
		{Name: "sync_remotes_add", Description: "Add sync remote", Method: http.MethodPost, Path: "/api/v1/sync/remotes", Resource: "sync", Action: "write", HasPayload: true},
		{Name: "sync_remotes_delete", Description: "Delete sync remote", Method: http.MethodDelete, Path: "/api/v1/sync/remotes/{name}", Resource: "sync", Action: "write"},
		{Name: "sync_push", Description: "Push sync payload", Method: http.MethodPost, Path: "/api/v1/sync/push", Resource: "sync", Action: "write", HasPayload: true},
		{Name: "sync_pull", Description: "Pull sync payload", Method: http.MethodPost, Path: "/api/v1/sync/pull", Resource: "sync", Action: "write", HasPayload: true},
		{Name: "sync_status", Description: "Get sync status", Method: http.MethodGet, Path: "/api/v1/sync/status", Resource: "sync", Action: "read", HasQuery: true},
		{Name: "sync_logs", Description: "Get sync logs", Method: http.MethodGet, Path: "/api/v1/sync/logs", Resource: "sync", Action: "read", HasQuery: true},
		{Name: "sync_diff_get", Description: "Get sync diff", Method: http.MethodGet, Path: "/api/v1/sync/diff", Resource: "sync", Action: "read", HasQuery: true},
		{Name: "sync_diff_post", Description: "Post sync diff request", Method: http.MethodPost, Path: "/api/v1/sync/diff", Resource: "sync", Action: "write", HasPayload: true},
		{Name: "sync_inbound_push", Description: "Inbound push endpoint", Method: http.MethodPost, Path: "/api/v1/sync/push/inbound", Resource: "sync", Action: "write", HasPayload: true},
		{Name: "sync_inbound_pull", Description: "Inbound pull endpoint", Method: http.MethodPost, Path: "/api/v1/sync/pull/inbound", Resource: "sync", Action: "write", HasPayload: true},
		{Name: "sync_conflicts_list", Description: "List sync conflicts", Method: http.MethodGet, Path: "/api/v1/sync/conflicts", Resource: "sync", Action: "read", HasQuery: true},
		{Name: "sync_conflicts_resolve", Description: "Resolve sync conflict", Method: http.MethodPost, Path: "/api/v1/sync/conflicts/{id}/resolve", Resource: "sync", Action: "write", HasPayload: true},

		// Admin
		{Name: "admin_stats", Description: "Get admin stats", Method: http.MethodGet, Path: "/api/v1/admin/stats", Resource: "admin", Action: "read"},
		{Name: "admin_config", Description: "Get admin config", Method: http.MethodGet, Path: "/api/v1/admin/config", Resource: "admin", Action: "read"},
		{Name: "admin_backup", Description: "Run backup", Method: http.MethodPost, Path: "/api/v1/admin/backup", Resource: "admin", Action: "write", HasPayload: true},
		{Name: "admin_vacuum", Description: "Run vacuum", Method: http.MethodPost, Path: "/api/v1/admin/vacuum", Resource: "admin", Action: "write", HasPayload: true},
		{Name: "admin_messages_purge_expired", Description: "Purge expired messages", Method: http.MethodDelete, Path: "/api/v1/admin/messages/expired", Resource: "admin", Action: "write"},
		{Name: "admin_rbac_roles", Description: "List RBAC roles", Method: http.MethodGet, Path: "/api/v1/admin/rbac/roles", Resource: "admin", Action: "manage"},
		{Name: "admin_rbac_assign", Description: "Assign RBAC role", Method: http.MethodPost, Path: "/api/v1/admin/rbac/assign", Resource: "admin", Action: "manage", HasPayload: true},
		{Name: "admin_rbac_revoke", Description: "Revoke RBAC role", Method: http.MethodDelete, Path: "/api/v1/admin/rbac/assign", Resource: "admin", Action: "manage", HasPayload: true},
	}
}

func (s ToolSpec) toTool() mcptypes.Tool {
	opts := []mcptypes.ToolOption{
		mcptypes.WithDescription(s.Description),
	}
	for _, param := range pathParams(s.Path) {
		opts = append(opts, mcptypes.WithString(param, mcptypes.Required(), mcptypes.Description("Path parameter: "+param)))
	}
	if s.HasQuery {
		opts = append(opts, mcptypes.WithObject("query", mcptypes.Description("Query string parameters")))
	}
	if s.HasPayload || methodHasBody(s.Method) {
		opts = append(opts, mcptypes.WithObject("payload", mcptypes.Description("JSON request payload")))
	}
	return mcptypes.NewTool(s.Name, opts...)
}

func (b *Bridge) makeToolHandler(spec ToolSpec) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
		token := extractBearer(request.Header.Get("Authorization"))
		if token == "" {
			token = b.defaultAPIKey
		}
		if token == "" {
			return mcptypes.NewToolResultError("missing API key: provide Authorization header or --api-key for stdio mode"), nil
		}

		authCtx, err := b.app.Authenticate(ctx, token)
		if err != nil {
			return mcptypes.NewToolResultError("authentication failed"), nil
		}
		if b.defaultAgentID != "" && authCtx.Agent.ID != b.defaultAgentID {
			return mcptypes.NewToolResultError("default agent mismatch for provided api key"), nil
		}
		if err := b.app.Authorize(authCtx, spec.Resource, spec.Action); err != nil {
			return mcptypes.NewToolResultError("forbidden: insufficient permissions"), nil
		}

		args := request.GetArguments()
		if args == nil {
			args = map[string]any{}
		}

		path, err := fillPath(spec.Path, args)
		if err != nil {
			return mcptypes.NewToolResultError(err.Error()), nil
		}

		var query map[string]any
		if spec.HasQuery {
			query = getArgMap(args, "query")
		}

		payload := getArgMap(args, "payload")
		if (spec.HasPayload || methodHasBody(spec.Method)) && payload == nil {
			payload = map[string]any{}
		}

		env, status, err := b.invokeREST(ctx, token, spec.Method, path, query, payload)
		if err != nil {
			return mcptypes.NewToolResultError(err.Error()), nil
		}
		if !env.OK {
			return mcptypes.NewToolResultError(apiErrorText(env.Error, status)), nil
		}

		out := map[string]any{
			"status_code": status,
			"data":        env.Data,
		}
		if env.Pagination != nil {
			out["pagination"] = env.Pagination
		}
		return mcptypes.NewToolResultJSON(out)
	}
}

func (b *Bridge) invokeREST(ctx context.Context, apiKey, method, path string, query map[string]any, payload map[string]any) (apiEnvelope, int, error) {
	target := path
	if len(query) > 0 {
		q := url.Values{}
		for k, v := range query {
			appendQueryValue(q, k, v)
		}
		qs := q.Encode()
		if qs != "" {
			target += "?" + qs
		}
	}

	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return apiEnvelope{}, 0, err
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, target, body).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	rr := httptest.NewRecorder()
	b.router.ServeHTTP(rr, req)

	var env apiEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		return apiEnvelope{}, rr.Code, fmt.Errorf("invalid API response: %w", err)
	}
	return env, rr.Code, nil
}

func fillPath(path string, args map[string]any) (string, error) {
	out := path
	for _, key := range pathParams(path) {
		value := strings.TrimSpace(argString(args, key))
		if value == "" {
			return "", fmt.Errorf("missing required path argument: %s", key)
		}
		out = strings.ReplaceAll(out, "{"+key+"}", url.PathEscape(value))
	}
	return out, nil
}

func pathParams(path string) []string {
	matches := routeParamPattern.FindAllStringSubmatch(path, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

func getArgMap(args map[string]any, key string) map[string]any {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case map[string]any:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			return nil
		}
		return out
	}
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		return fmt.Sprint(v)
	}
	if pm := getArgMap(args, "path"); pm != nil {
		if v, ok := pm[key]; ok {
			return fmt.Sprint(v)
		}
	}
	return ""
}

func methodHasBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func appendQueryValue(q url.Values, key string, raw any) {
	switch v := raw.(type) {
	case nil:
		return
	case []string:
		for _, it := range v {
			q.Add(key, it)
		}
	case []any:
		for _, it := range v {
			q.Add(key, fmt.Sprint(it))
		}
	default:
		q.Add(key, fmt.Sprint(v))
	}
}

func apiErrorText(apiErr any, status int) string {
	if m, ok := apiErr.(map[string]any); ok {
		code := fmt.Sprint(m["code"])
		msg := fmt.Sprint(m["message"])
		if code != "" && msg != "" {
			return code + ": " + msg
		}
		if msg != "" {
			return msg
		}
	}
	if apiErr != nil {
		return fmt.Sprint(apiErr)
	}
	return fmt.Sprintf("request failed with status %d", status)
}

func extractBearer(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if strings.HasPrefix(strings.ToLower(h), strings.ToLower(prefix)) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return h
}
