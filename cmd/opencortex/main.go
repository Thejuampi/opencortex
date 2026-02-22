package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

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
	"opencortex/internal/webui"
)

type apiClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newAPIClient(baseURL, apiKey string) *apiClient {
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *apiClient) do(method, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if !envelope.OK {
		if len(envelope.Error) == 0 {
			return fmt.Errorf("request failed: status %d", resp.StatusCode)
		}
		return fmt.Errorf("request failed: %s", envelope.Error)
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func main() {
	var (
		cfgPath string
		baseURL string
		apiKey  string
		asJSON  bool
	)

	root := &cobra.Command{
		Use:   "opencortex",
		Short: "Opencortex self-hosted multi-agent infrastructure node",
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", "config.yaml", "Path to config file")
	root.PersistentFlags().StringVar(&baseURL, "base-url", "http://localhost:8080", "Base API URL for CLI commands")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for CLI commands")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Print JSON output")

	root.AddCommand(newInitCommand(&cfgPath))
	root.AddCommand(newServerCommand(&cfgPath))
	root.AddCommand(newAgentsCommand(&baseURL, &apiKey, &asJSON))
	root.AddCommand(newKnowledgeCommand(&baseURL, &apiKey, &asJSON))
	root.AddCommand(newSyncCommand(&baseURL, &apiKey, &asJSON))
	root.AddCommand(newAdminCommand(&baseURL, &apiKey, &asJSON))

	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

func newInitCommand(cfgPath *string) *cobra.Command {
	var adminName string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize local opencortex state and create admin key",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigMaybe(*cfgPath)
			if err != nil {
				return err
			}
			ctx := context.Background()
			db, err := storage.Open(ctx, cfg)
			if err != nil {
				return err
			}
			defer db.Close()
			if err := storage.Migrate(ctx, db); err != nil {
				return err
			}

			store := repos.New(db)
			memBroker := broker.NewMemory(cfg.Broker.ChannelBufferSize)
			app := service.New(cfg, store, memBroker)
			agent, key, err := app.BootstrapInit(ctx, adminName)
			if err != nil {
				return err
			}
			fmt.Printf("Initialized at %s\n", filepath.Clean(cfg.Database.Path))
			fmt.Printf("Admin Agent ID: %s\n", agent.ID)
			fmt.Printf("Admin API Key (shown once): %s\n", key)
			return nil
		},
	}
	cmd.Flags().StringVar(&adminName, "admin-name", "admin", "Name for bootstrap admin agent")
	return cmd
}

func newServerCommand(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Run opencortex server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigMaybe(*cfgPath)
			if err != nil {
				return err
			}
			ctx := context.Background()
			db, err := storage.Open(ctx, cfg)
			if err != nil {
				return err
			}
			if err := storage.Migrate(ctx, db); err != nil {
				return err
			}
			store := repos.New(db)
			if err := store.SeedRBAC(ctx); err != nil {
				return err
			}
			memBroker := broker.NewMemory(cfg.Broker.ChannelBufferSize)
			app := service.New(cfg, store, memBroker)
			syncEngine := syncer.NewEngine(db, store)
			handler := handlers.New(app, db, cfg, syncEngine)
			hub := ws.NewHub(app, store)
			hub.Start(ctx)

			if cfg.Sync.Enabled && len(cfg.Sync.Remotes) > 0 {
				for _, r := range cfg.Sync.Remotes {
					_, _ = app.AddRemote(ctx, repos.CreateRemoteInput{
						RemoteName: r.Name,
						RemoteURL:  r.URL,
						Direction:  model.SyncDirection(r.Sync.Direction),
						Scope:      model.SyncScope(r.Sync.Scope),
						ScopeIDs:   r.Sync.CollectionIDs,
						Strategy:   r.Sync.ConflictStrategy,
						Schedule:   valuePtr(r.Sync.Schedule),
					}, r.Key)
				}
				scheduler := syncer.NewScheduler(syncEngine)
				if err := scheduler.Register(cfg.Sync.Remotes); err != nil {
					return err
				}
				scheduler.Start()
				defer scheduler.Stop()
			}

			apiRouter := api.NewRouter(handler, app, hub)
			uiFS := http.FileServer(http.FS(webui.FS()))
			serverMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/api/"), r.URL.Path == "/healthz":
					apiRouter.ServeHTTP(w, r)
				default:
					uiFS.ServeHTTP(w, r)
				}
			})
			httpServer := &http.Server{
				Addr:         config.Addr(cfg),
				Handler:      serverMux,
				ReadTimeout:  config.ReadTimeout(cfg),
				WriteTimeout: config.WriteTimeout(cfg),
			}
			log.Printf("opencortex server listening on %s", config.Addr(cfg))
			return httpServer.ListenAndServe()
		},
	}
}

func newAgentsCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{Use: "agents", Short: "Agent management commands"}
	cmd.AddCommand(&cobra.Command{
		Use: "list",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out struct {
				Agents []model.Agent `json:"agents"`
			}
			if err := client.do(http.MethodGet, "/api/v1/agents", nil, &out); err != nil {
				return err
			}
			if *asJSON {
				return printJSON(out)
			}
			return printAgentsTable(out.Agents)
		},
	})

	var createName, createType, createDesc string
	var createTags []string
	cmdCreate := &cobra.Command{
		Use: "create",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			err := client.do(http.MethodPost, "/api/v1/agents", map[string]any{
				"name":        createName,
				"type":        createType,
				"description": createDesc,
				"tags":        createTags,
			}, &out)
			if err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdCreate.Flags().StringVar(&createName, "name", "", "Agent name")
	cmdCreate.Flags().StringVar(&createType, "type", "ai", "Agent type: human|ai|system")
	cmdCreate.Flags().StringVar(&createDesc, "description", "", "Description")
	cmdCreate.Flags().StringSliceVar(&createTags, "tags", nil, "Tags")
	_ = cmdCreate.MarkFlagRequired("name")
	cmd.AddCommand(cmdCreate)

	cmd.AddCommand(&cobra.Command{
		Use:  "get <id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/agents/"+args[0], nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:  "rotate-key <id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/agents/"+args[0]+"/rotate-key", map[string]any{}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	return cmd
}

func newKnowledgeCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{Use: "knowledge", Short: "Knowledge base commands"}
	cmd.AddCommand(&cobra.Command{
		Use:  "search <query>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/knowledge?q="+args[0], nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	var title, filePath string
	var tags []string
	cmdAdd := &cobra.Command{
		Use: "add",
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/knowledge", map[string]any{
				"title":   title,
				"content": string(content),
				"tags":    tags,
			}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdAdd.Flags().StringVar(&title, "title", "", "Knowledge title")
	cmdAdd.Flags().StringVar(&filePath, "file", "", "File path")
	cmdAdd.Flags().StringSliceVar(&tags, "tags", nil, "Tags")
	_ = cmdAdd.MarkFlagRequired("title")
	_ = cmdAdd.MarkFlagRequired("file")
	cmd.AddCommand(cmdAdd)

	cmd.AddCommand(&cobra.Command{
		Use:  "get <id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/knowledge/"+args[0], nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	var exportCollection, exportFormat, exportOut string
	cmdExport := &cobra.Command{
		Use: "export",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			path := "/api/v1/knowledge"
			if exportCollection != "" {
				path += "?collection_id=" + exportCollection
			}
			if err := client.do(http.MethodGet, path, nil, &out); err != nil {
				return err
			}
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}
			if exportFormat == "json" && exportOut != "" {
				return os.WriteFile(exportOut, b, 0o644)
			}
			fmt.Println(string(b))
			return nil
		},
	}
	cmdExport.Flags().StringVar(&exportCollection, "collection", "", "Collection id")
	cmdExport.Flags().StringVar(&exportFormat, "format", "json", "Export format")
	cmdExport.Flags().StringVar(&exportOut, "out", "", "Output file")
	cmd.AddCommand(cmdExport)

	var importFile string
	cmdImport := &cobra.Command{
		Use: "import",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(importFile)
			if err != nil {
				return err
			}
			var payload map[string]any
			if err := json.Unmarshal(b, &payload); err != nil {
				return err
			}
			client := newAPIClient(*baseURL, *apiKey)
			if items, ok := payload["knowledge"].([]any); ok {
				for _, item := range items {
					_ = client.do(http.MethodPost, "/api/v1/knowledge", item, nil)
				}
			}
			fmt.Println("import complete")
			return nil
		},
	}
	cmdImport.Flags().StringVar(&importFile, "file", "", "Import file")
	_ = cmdImport.MarkFlagRequired("file")
	cmd.AddCommand(cmdImport)
	return cmd
}

func newSyncCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "Sync commands"}

	remoteCmd := &cobra.Command{Use: "remote", Short: "Remote management"}
	var remoteName, remoteURL, remoteKey string
	addRemote := &cobra.Command{
		Use:  "add <name> <url>",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			err := client.do(http.MethodPost, "/api/v1/sync/remotes", map[string]any{
				"name":    args[0],
				"url":     args[1],
				"api_key": remoteKey,
			}, &out)
			if err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	addRemote.Flags().StringVar(&remoteName, "name", "", "Remote name (alias)")
	addRemote.Flags().StringVar(&remoteURL, "url", "", "Remote url")
	addRemote.Flags().StringVar(&remoteKey, "key", "", "Remote API key")
	remoteCmd.AddCommand(addRemote)

	remoteCmd.AddCommand(&cobra.Command{
		Use: "list",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/sync/remotes", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	cmd.AddCommand(remoteCmd)

	cmd.AddCommand(syncPushPullCommand("push", baseURL, apiKey))
	cmd.AddCommand(syncPushPullCommand("pull", baseURL, apiKey))

	cmd.AddCommand(&cobra.Command{
		Use: "status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/sync/status", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:  "diff <remote>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/sync/diff?remote="+args[0], nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "conflicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/sync/conflicts", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	var strategy string
	resolveCmd := &cobra.Command{
		Use:  "resolve <conflict-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/sync/conflicts/"+args[0]+"/resolve", map[string]any{
				"strategy": strategy,
			}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	resolveCmd.Flags().StringVar(&strategy, "strategy", "latest-wins", "Conflict strategy")
	cmd.AddCommand(resolveCmd)

	_ = asJSON
	return cmd
}

func syncPushPullCommand(kind string, baseURL, apiKey *string) *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use:  kind + " <remote>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			body := map[string]any{
				"remote":  args[0],
				"scope":   "full",
				"api_key": key,
			}
			if err := client.do(http.MethodPost, "/api/v1/sync/"+kind, body, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "Remote API key")
	return cmd
}

func newAdminCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{Use: "admin", Short: "Admin commands"}
	cmd.AddCommand(adminSimpleCommand("stats", "/api/v1/admin/stats", baseURL, apiKey))
	cmd.AddCommand(adminSimpleCommand("backup", "/api/v1/admin/backup", baseURL, apiKey))
	cmd.AddCommand(adminSimpleCommand("vacuum", "/api/v1/admin/vacuum", baseURL, apiKey))

	rbac := &cobra.Command{Use: "rbac", Short: "RBAC commands"}
	rbac.AddCommand(adminSimpleCommand("roles", "/api/v1/admin/rbac/roles", baseURL, apiKey))

	var agentID, role string
	assign := &cobra.Command{
		Use: "assign",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/admin/rbac/assign", map[string]any{
				"agent_id": agentID,
				"role":     role,
			}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	assign.Flags().StringVar(&agentID, "agent", "", "Agent ID")
	assign.Flags().StringVar(&role, "role", "", "Role")
	_ = assign.MarkFlagRequired("agent")
	_ = assign.MarkFlagRequired("role")
	rbac.AddCommand(assign)

	revoke := &cobra.Command{
		Use: "revoke",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodDelete, "/api/v1/admin/rbac/assign", map[string]any{
				"agent_id": agentID,
				"role":     role,
			}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	revoke.Flags().StringVar(&agentID, "agent", "", "Agent ID")
	revoke.Flags().StringVar(&role, "role", "", "Role")
	_ = revoke.MarkFlagRequired("agent")
	_ = revoke.MarkFlagRequired("role")
	rbac.AddCommand(revoke)
	cmd.AddCommand(rbac)
	_ = asJSON
	return cmd
}

func adminSimpleCommand(use, path string, baseURL, apiKey *string) *cobra.Command {
	method := http.MethodGet
	if strings.Contains(path, "backup") || strings.Contains(path, "vacuum") {
		method = http.MethodPost
	}
	return &cobra.Command{
		Use: use,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(method, path, map[string]any{}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
}

func loadConfigMaybe(path string) (config.Config, error) {
	if path == "" {
		return config.Default(), nil
	}
	if _, err := os.Stat(path); err == nil {
		return config.Load(path)
	} else if errors.Is(err, os.ErrNotExist) {
		return config.Default(), nil
	} else {
		return config.Config{}, err
	}
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func printAgentsTable(items []model.Agent) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tSTATUS\tCREATED_AT")
	for _, a := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Type, a.Status, a.CreatedAt.Format(time.RFC3339))
	}
	return w.Flush()
}

func valuePtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}
