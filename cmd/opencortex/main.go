package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

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
	baseURL = canonicalBaseURL(baseURL)
	if strings.TrimSpace(apiKey) == "" {
		if saved, ok := authStoreGetToken(baseURL); ok {
			apiKey = saved
		}
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
		Long:  "Single-binary platform for multi-agent messaging, knowledge management, and selective sync.",
		Example: strings.TrimSpace(`
  opencortex init --config ./config.yaml
  opencortex server --config ./config.yaml
  opencortex agents list --api-key <admin-key>
  opencortex knowledge search "sync conflicts" --api-key <key>
  opencortex sync status --api-key <sync-key>`),
	}
	root.SetHelpFunc(renderHelp)
	root.PersistentFlags().StringVar(&cfgPath, "config", "config.yaml", "Path to config file")
	root.PersistentFlags().StringVar(&baseURL, "base-url", "http://localhost:8080", "Base API URL for CLI commands")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for CLI commands")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Print JSON output")

	root.AddCommand(newInitCommand(&cfgPath))
	root.AddCommand(newServerCommand(&cfgPath))
	root.AddCommand(newConfigCommand(&cfgPath))
	root.AddCommand(newAuthCommand(&baseURL, &apiKey, &asJSON))
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
		Example: strings.TrimSpace(`
  opencortex init --config ./config.yaml
  opencortex init --admin-name "platform-admin"`),
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
		Example: strings.TrimSpace(`
  opencortex server
  opencortex server --config ./config.yaml`),
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
			uiAssets := webui.FS()
			uiFS := http.FileServer(http.FS(uiAssets))
			serverMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/api/"), r.URL.Path == "/healthz":
					apiRouter.ServeHTTP(w, r)
				default:
					cleanPath := strings.TrimPrefix(r.URL.Path, "/")
					if cleanPath == "" {
						uiFS.ServeHTTP(w, r)
						return
					}
					_, err := iofs.Stat(uiAssets, cleanPath)
					if err == nil {
						uiFS.ServeHTTP(w, r)
						return
					}
					if errors.Is(err, iofs.ErrNotExist) {
						http.ServeFileFS(w, r, uiAssets, "index.html")
						return
					}
					http.Error(w, "internal ui routing error", http.StatusInternalServerError)
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

func newConfigCommand(cfgPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config file tools and schema help",
		Long: strings.TrimSpace(`
Configuration scheme overview:
  mode: local | server | hybrid
  server: host/port/timeouts/cors
  database: sqlite path, WAL, pool, backup
  auth: toggle auth and admin bootstrap key
  broker: channel buffer, TTL default, max message size
  knowledge: max entry size, FTS, version settings
  sync: remotes and sync strategy/scope
  ui: embedded web UI options
  logging: level/format/output file

Environment overrides:
  OPENCORTEX_MODE
  OPENCORTEX_SERVER_HOST
  OPENCORTEX_SERVER_PORT
  OPENCORTEX_DB_PATH
  OPENCORTEX_AUTH_ENABLED
  AGENTMESH_ADMIN_KEY`),
		Example: strings.TrimSpace(`
  opencortex config --help
  opencortex config example > config.yaml
  opencortex config wizard --out ./config.local.yaml
  opencortex config validate --config ./config.yaml`),
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "example",
		Short: "Print a complete example config YAML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Default()
			b, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Print(string(b))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate a config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			fmt.Printf("config ok: %s\n", filepath.Clean(*cfgPath))
			return nil
		},
	})

	var out string
	wizard := &cobra.Command{
		Use:   "wizard",
		Short: "Run an interactive config generator",
		Example: strings.TrimSpace(`
  opencortex config wizard
  opencortex config wizard --out ./config.prod.yaml`),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Default()
			reader := bufio.NewReader(os.Stdin)

			fmt.Println("Opencortex config wizard")
			fmt.Println("Press Enter to accept defaults shown in [brackets].")
			fmt.Println("")

			cfg.Mode = promptChoice(reader, "mode", cfg.Mode, []string{"local", "server", "hybrid"})
			cfg.Server.Host = prompt(reader, "server.host", cfg.Server.Host)
			cfg.Server.Port = promptInt(reader, "server.port", cfg.Server.Port)
			cfg.Server.ReadTimeout = prompt(reader, "server.read_timeout", cfg.Server.ReadTimeout)
			cfg.Server.WriteTimeout = prompt(reader, "server.write_timeout", cfg.Server.WriteTimeout)
			cfg.Server.CORSOrigins = promptCSV(reader, "server.cors_origins (comma-separated)", cfg.Server.CORSOrigins)

			cfg.Database.Path = prompt(reader, "database.path", cfg.Database.Path)
			cfg.Database.WALMode = promptBool(reader, "database.wal_mode", cfg.Database.WALMode)
			cfg.Database.MaxConnections = promptInt(reader, "database.max_connections", cfg.Database.MaxConnections)
			cfg.Database.BackupInterval = prompt(reader, "database.backup_interval", cfg.Database.BackupInterval)
			cfg.Database.BackupPath = prompt(reader, "database.backup_path", cfg.Database.BackupPath)

			cfg.Auth.Enabled = promptBool(reader, "auth.enabled", cfg.Auth.Enabled)
			cfg.Auth.AdminKey = prompt(reader, "auth.admin_key (optional)", cfg.Auth.AdminKey)
			cfg.Auth.TokenExpiry = prompt(reader, "auth.token_expiry", cfg.Auth.TokenExpiry)

			cfg.Broker.ChannelBufferSize = promptInt(reader, "broker.channel_buffer_size", cfg.Broker.ChannelBufferSize)
			cfg.Broker.MessageTTLDefault = prompt(reader, "broker.message_ttl_default", cfg.Broker.MessageTTLDefault)
			cfg.Broker.MaxMessageSizeKB = promptInt(reader, "broker.max_message_size_kb", cfg.Broker.MaxMessageSizeKB)

			cfg.Knowledge.MaxEntrySizeKB = promptInt(reader, "knowledge.max_entry_size_kb", cfg.Knowledge.MaxEntrySizeKB)
			cfg.Knowledge.FTSEnabled = promptBool(reader, "knowledge.fts_enabled", cfg.Knowledge.FTSEnabled)
			cfg.Knowledge.VersionHistory = promptBool(reader, "knowledge.version_history", cfg.Knowledge.VersionHistory)
			cfg.Knowledge.MaxVersionsKept = promptInt(reader, "knowledge.max_versions_kept", cfg.Knowledge.MaxVersionsKept)

			cfg.Sync.Enabled = promptBool(reader, "sync.enabled", cfg.Sync.Enabled)
			cfg.UI.Enabled = promptBool(reader, "ui.enabled", cfg.UI.Enabled)
			cfg.UI.Title = prompt(reader, "ui.title", cfg.UI.Title)
			cfg.UI.Theme = promptChoice(reader, "ui.theme", cfg.UI.Theme, []string{"light", "dark", "auto"})
			cfg.Logging.Level = promptChoice(reader, "logging.level", cfg.Logging.Level, []string{"debug", "info", "warn", "error"})
			cfg.Logging.Format = promptChoice(reader, "logging.format", cfg.Logging.Format, []string{"json", "text"})
			cfg.Logging.File = prompt(reader, "logging.file (empty=stdout)", cfg.Logging.File)

			if err := validateWizardConfig(cfg); err != nil {
				return err
			}

			b, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}

			target := out
			if strings.TrimSpace(target) == "" {
				target = *cfgPath
			}
			if strings.TrimSpace(target) == "" {
				target = "config.yaml"
			}
			if err := os.WriteFile(target, b, 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote config: %s\n", filepath.Clean(target))
			fmt.Println("next: opencortex config validate --config " + target)
			return nil
		},
	}
	wizard.Flags().StringVar(&out, "out", "", "Output path (default: --config value or config.yaml)")
	cmd.AddCommand(wizard)
	return cmd
}

func newAuthCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate CLI and persist credentials",
		Long: strings.TrimSpace(`
Persistent auth works similarly to gh:
  - login stores credentials for a host/base URL
  - status shows saved accounts and current selection
  - whoami validates current credentials against /api/v1/agents/me
  - switch changes the active saved account
  - logout removes one or all saved accounts

When --api-key is omitted, CLI commands use the saved account for --base-url.`),
		Example: strings.TrimSpace(`
  opencortex auth login --base-url http://localhost:8080 --api-key amk_live_xxx
  opencortex auth status
  opencortex auth whoami
  opencortex auth switch --base-url https://hub.example.com
  opencortex auth logout --base-url http://localhost:8080`),
	}

	var withToken bool
	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Login and store credentials for a base URL",
		RunE: func(cmd *cobra.Command, args []string) error {
			key := strings.TrimSpace(*apiKey)
			if withToken {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				key = strings.TrimSpace(string(b))
			}
			if key == "" {
				key = prompt(bufio.NewReader(os.Stdin), "API key", "")
			}
			if key == "" {
				return errors.New("api key is required")
			}

			client := newAPIClient(*baseURL, key)
			var out struct {
				Agent any      `json:"agent"`
				Roles []string `json:"roles"`
			}
			if err := client.do(http.MethodGet, "/api/v1/agents/me", nil, &out); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			if err := authStoreUpsert(client.baseURL, key, out.Agent, out.Roles); err != nil {
				return err
			}
			fmt.Printf("logged in to %s\n", client.baseURL)
			if *asJSON {
				return printJSON(map[string]any{
					"base_url": client.baseURL,
					"agent":    out.Agent,
					"roles":    out.Roles,
				})
			}
			fmt.Println("credentials saved")
			return nil
		},
	}
	loginCmd.Flags().BoolVar(&withToken, "with-token", false, "Read API key from stdin")
	cmd.AddCommand(loginCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show saved auth accounts and active selection",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			if *asJSON {
				type entry struct {
					BaseURL    string    `json:"base_url"`
					AgentID    string    `json:"agent_id,omitempty"`
					AgentName  string    `json:"agent_name,omitempty"`
					Roles      []string  `json:"roles,omitempty"`
					UpdatedAt  time.Time `json:"updated_at"`
					HasToken   bool      `json:"has_token"`
					IsCurrent  bool      `json:"is_current"`
				}
				out := make([]entry, 0, len(store.Accounts))
				for k, v := range store.Accounts {
					out = append(out, entry{
						BaseURL:   k,
						AgentID:   v.AgentID,
						AgentName: v.AgentName,
						Roles:     v.Roles,
						UpdatedAt: v.UpdatedAt,
						HasToken:  strings.TrimSpace(v.APIKey) != "",
						IsCurrent: store.Current == k,
					})
				}
				sort.Slice(out, func(i, j int) bool { return out[i].BaseURL < out[j].BaseURL })
				return printJSON(map[string]any{
					"current":  store.Current,
					"accounts": out,
				})
			}
			if len(store.Accounts) == 0 {
				fmt.Println("no saved auth accounts")
				return nil
			}
			keys := make([]string, 0, len(store.Accounts))
			for k := range store.Accounts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				a := store.Accounts[k]
				active := " "
				if store.Current == k {
					active = "*"
				}
				fmt.Printf("%s %s", active, k)
				if a.AgentName != "" {
					fmt.Printf("  (%s)", a.AgentName)
				}
				if a.AgentID != "" {
					fmt.Printf(" [%s]", a.AgentID)
				}
				fmt.Println()
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "whoami",
		Short: "Show current authenticated agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			if strings.TrimSpace(client.apiKey) == "" {
				return errors.New("not logged in for this base-url; run 'opencortex auth login'")
			}
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/agents/me", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	switchCmd := &cobra.Command{
		Use:   "switch",
		Short: "Switch active saved account to --base-url",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			if _, ok := store.Accounts[base]; !ok {
				return fmt.Errorf("no saved account for %s", base)
			}
			store.Current = base
			if err := authStoreSave(store); err != nil {
				return err
			}
			fmt.Printf("active account: %s\n", base)
			return nil
		},
	}
	cmd.AddCommand(switchCmd)

	var logoutAll bool
	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove saved auth account",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			if logoutAll {
				store.Accounts = map[string]authAccount{}
				store.Current = ""
				if err := authStoreSave(store); err != nil {
					return err
				}
				fmt.Println("removed all saved accounts")
				return nil
			}
			base := canonicalBaseURL(*baseURL)
			delete(store.Accounts, base)
			if store.Current == base {
				store.Current = ""
			}
			if store.Current == "" && len(store.Accounts) > 0 {
				for k := range store.Accounts {
					store.Current = k
					break
				}
			}
			if err := authStoreSave(store); err != nil {
				return err
			}
			fmt.Printf("logged out from %s\n", base)
			return nil
		},
	}
	logoutCmd.Flags().BoolVar(&logoutAll, "all", false, "Remove all saved accounts")
	cmd.AddCommand(logoutCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "token",
		Short: "Print stored token for --base-url (or current account)",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			token, ok := authStoreGetToken(base)
			if !ok && base == canonicalBaseURL("http://localhost:8080") {
				token, ok = authStoreGetToken("")
			}
			if !ok {
				return errors.New("no saved token; run 'opencortex auth login'")
			}
			fmt.Println(token)
			return nil
		},
	})

	return cmd
}

func newAgentsCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Agent management commands",
		Example: strings.TrimSpace(`
  opencortex agents list --api-key <admin-key>
  opencortex agents create --name researcher --type ai --tags coder,planner --api-key <admin-key>
  opencortex agents rotate-key <agent-id> --api-key <admin-key>`),
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List agents",
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
		Use:   "create",
		Short: "Create an agent",
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
		Use:   "get <id>",
		Short: "Get agent details",
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
		Use:   "rotate-key <id>",
		Short: "Rotate an agent API key",
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
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "Knowledge base commands",
		Example: strings.TrimSpace(`
  opencortex knowledge search "quantum" --api-key <key>
  opencortex knowledge add --title "Design Notes" --file ./notes.md --tags architecture --api-key <key>
  opencortex knowledge add --title "Ops Runbook" --file ./runbook.md --summary "Deployment and rollback flow" --api-key <key>`),
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "search <query>",
		Short: "Search knowledge entries",
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
	var summary string
	cmdAdd := &cobra.Command{
		Use:   "add",
		Short: "Add a knowledge entry from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"title":   title,
				"content": string(content),
				"tags":    tags,
			}
			if strings.TrimSpace(summary) != "" {
				payload["summary"] = summary
			}
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/knowledge", payload, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdAdd.Flags().StringVar(&title, "title", "", "Knowledge title")
	cmdAdd.Flags().StringVar(&filePath, "file", "", "File path")
	cmdAdd.Flags().StringSliceVar(&tags, "tags", nil, "Tags")
	cmdAdd.Flags().StringVar(&summary, "summary", "", "Optional abstract/summary (recommended)")
	_ = cmdAdd.MarkFlagRequired("title")
	_ = cmdAdd.MarkFlagRequired("file")
	cmd.AddCommand(cmdAdd)

	cmd.AddCommand(&cobra.Command{
		Use:   "get <id>",
		Short: "Get a knowledge entry",
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
		Use:   "export",
		Short: "Export knowledge entries",
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
		Use:   "import",
		Short: "Import knowledge entries from JSON",
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
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync commands",
		Example: strings.TrimSpace(`
  opencortex sync remote add origin https://hub.example.com --key amk_remote_xxx --api-key <sync-key>
  opencortex sync diff origin --api-key <sync-key>
  opencortex sync push origin --key amk_remote_xxx --api-key <sync-key>
  opencortex sync conflicts --api-key <sync-key>`),
	}

	remoteCmd := &cobra.Command{
		Use:   "remote",
		Short: "Remote management",
		Example: strings.TrimSpace(`
  opencortex sync remote add origin https://hub.example.com --key amk_remote_xxx --api-key <sync-key>
  opencortex sync remote list --api-key <sync-key>`),
	}
	var remoteName, remoteURL, remoteKey string
	addRemote := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add a sync remote",
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
		Use:   "list",
		Short: "List configured remotes",
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
		Use:   "status",
		Short: "Show sync status",
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
		Use:   "diff <remote>",
		Short: "Preview sync changes for a remote",
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
		Use:   "conflicts",
		Short: "List unresolved sync conflicts",
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
		Use:   "resolve <conflict-id>",
		Short: "Resolve a sync conflict",
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
	short := "Push data to remote"
	if kind == "pull" {
		short = "Pull data from remote"
	}
	cmd := &cobra.Command{
		Use:   kind + " <remote>",
		Short: short,
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
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Admin commands",
		Example: strings.TrimSpace(`
  opencortex admin stats --api-key <admin-key>
  opencortex admin backup --api-key <admin-key>
  opencortex admin rbac roles --api-key <admin-key>
  opencortex admin rbac assign --agent <agent-id> --role agent --api-key <admin-key>`),
	}
	cmd.AddCommand(adminSimpleCommand("stats", "/api/v1/admin/stats", baseURL, apiKey))
	cmd.AddCommand(adminSimpleCommand("backup", "/api/v1/admin/backup", baseURL, apiKey))
	cmd.AddCommand(adminSimpleCommand("vacuum", "/api/v1/admin/vacuum", baseURL, apiKey))

	rbac := &cobra.Command{Use: "rbac", Short: "RBAC commands"}
	rbac.AddCommand(adminSimpleCommand("roles", "/api/v1/admin/rbac/roles", baseURL, apiKey))

	var agentID, role string
	assign := &cobra.Command{
		Use:   "assign",
		Short: "Assign role to an agent",
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
		Use:   "revoke",
		Short: "Revoke role from an agent",
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
	short := simpleTitle(use)
	return &cobra.Command{
		Use:   use,
		Short: short,
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

type authAccount struct {
	BaseURL   string    `json:"base_url"`
	APIKey    string    `json:"api_key"`
	AgentID   string    `json:"agent_id,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	Roles     []string  `json:"roles,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type authStore struct {
	Current  string                 `json:"current"`
	Accounts map[string]authAccount `json:"accounts"`
}

func canonicalBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "http://localhost:8080"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func authStorePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		home, hErr := os.UserHomeDir()
		if hErr != nil {
			return "", hErr
		}
		base = filepath.Join(home, ".opencortex")
	} else {
		base = filepath.Join(base, "opencortex")
	}
	if mkErr := os.MkdirAll(base, 0o700); mkErr != nil {
		return "", mkErr
	}
	return filepath.Join(base, "auth.json"), nil
}

func authStoreLoad() (authStore, error) {
	path, err := authStorePath()
	if err != nil {
		return authStore{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return authStore{Accounts: map[string]authAccount{}}, nil
		}
		return authStore{}, err
	}
	var s authStore
	if err := json.Unmarshal(b, &s); err != nil {
		return authStore{}, err
	}
	if s.Accounts == nil {
		s.Accounts = map[string]authAccount{}
	}
	return s, nil
}

func authStoreSave(store authStore) error {
	path, err := authStorePath()
	if err != nil {
		return err
	}
	if store.Accounts == nil {
		store.Accounts = map[string]authAccount{}
	}
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func authStoreUpsert(baseURL, key string, agent any, roles []string) error {
	baseURL = canonicalBaseURL(baseURL)
	store, err := authStoreLoad()
	if err != nil {
		return err
	}

	acc := authAccount{
		BaseURL:   baseURL,
		APIKey:    key,
		Roles:     roles,
		UpdatedAt: time.Now().UTC(),
	}
	if m, ok := agent.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			acc.AgentID = id
		}
		if name, ok := m["name"].(string); ok {
			acc.AgentName = name
		}
	}

	store.Accounts[baseURL] = acc
	store.Current = baseURL
	return authStoreSave(store)
}

func authStoreGetToken(baseURL string) (string, bool) {
	store, err := authStoreLoad()
	if err != nil {
		return "", false
	}
	baseURL = canonicalBaseURL(baseURL)
	if baseURL == "" && store.Current != "" {
		baseURL = store.Current
	}
	if baseURL != "" {
		if acc, ok := store.Accounts[baseURL]; ok && strings.TrimSpace(acc.APIKey) != "" {
			return acc.APIKey, true
		}
	}
	if store.Current != "" {
		if acc, ok := store.Accounts[store.Current]; ok && strings.TrimSpace(acc.APIKey) != "" {
			return acc.APIKey, true
		}
	}
	return "", false
}

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
	ansiBlue  = "\x1b[34m"
	ansiGreen = "\x1b[32m"
	ansiGray  = "\x1b[90m"
)

func renderHelp(cmd *cobra.Command, _ []string) {
	b := &strings.Builder{}

	fmt.Fprintf(b, "%s%sOpenCortex CLI%s\n", ansiBold, ansiCyan, ansiReset)
	if cmd.CommandPath() == "opencortex" {
		fmt.Fprintf(b, "%sMulti-agent messaging, knowledge, sync, and operations in one binary.%s\n\n", ansiDim, ansiReset)
	}

	fmt.Fprintf(b, "%sUSAGE%s\n", sectionColor(), ansiReset)
	fmt.Fprintf(b, "  %s\n\n", cmd.UseLine())

	if strings.TrimSpace(cmd.Long) != "" && cmd.Long != cmd.Short {
		fmt.Fprintf(b, "%sOVERVIEW%s\n%s\n\n", sectionColor(), ansiReset, indentBlock(strings.TrimSpace(cmd.Long), "  "))
	}

	if cmd.CommandPath() == "opencortex" {
		fmt.Fprintf(b, "%sWHAT YOU CAN DO%s\n", sectionColor(), ansiReset)
		fmt.Fprintf(b, "  %sLifecycle%s: initialize local state, run API+WS+UI server\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sAgents%s: register, inspect, rotate keys\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sKnowledge%s: add, search, version, export/import entries\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sSync%s: configure remotes, diff, push/pull, resolve conflicts\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sAdmin%s: stats, backups, vacuum, RBAC role assignment\n\n", ansiGreen, ansiReset)
	}

	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(b, "%sCOMMANDS%s\n", sectionColor(), ansiReset)
		sub := availableSubcommands(cmd)
		w := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
		for _, c := range sub {
			fmt.Fprintf(w, "  %s%s%s\t%s\n", ansiBlue, c.Name(), ansiReset, c.Short)
		}
		_ = w.Flush()
		fmt.Fprintln(b)
	}

	flagText := strings.TrimSpace(cmd.NonInheritedFlags().FlagUsagesWrapped(100))
	if flagText != "" {
		fmt.Fprintf(b, "%sFLAGS%s\n%s\n\n", sectionColor(), ansiReset, indentBlock(flagText, "  "))
	}
	inherited := strings.TrimSpace(cmd.InheritedFlags().FlagUsagesWrapped(100))
	if inherited != "" {
		fmt.Fprintf(b, "%sGLOBAL FLAGS%s\n%s\n\n", sectionColor(), ansiReset, indentBlock(inherited, "  "))
	}

	if strings.TrimSpace(cmd.Example) != "" {
		fmt.Fprintf(b, "%sEXAMPLES%s\n%s\n\n", sectionColor(), ansiReset, indentBlock(cmd.Example, "  "))
	}

	if cmd.CommandPath() == "opencortex" {
		fmt.Fprintf(b, "%sQUICK WORKFLOWS%s\n", sectionColor(), ansiReset)
		fmt.Fprintf(b, "  %sBoot and access UI%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex init --config ./config.yaml\n")
		fmt.Fprintf(b, "    opencortex server --config ./config.yaml\n")
		fmt.Fprintf(b, "    # open http://localhost:8080\n\n")

		fmt.Fprintf(b, "  %sPush first knowledge item%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex knowledge add --title \"System Scope\" --file ./scope.md --tags scope,mvp --api-key <key>\n")
		fmt.Fprintf(b, "    opencortex knowledge search \"scope\" --api-key <key>\n\n")

		fmt.Fprintf(b, "  %sSync with remote hub%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex sync remote add origin https://hub.example.com --key amk_remote_xxx --api-key <sync-key>\n")
		fmt.Fprintf(b, "    opencortex sync diff origin --api-key <sync-key>\n")
		fmt.Fprintf(b, "    opencortex sync push origin --key amk_remote_xxx --api-key <sync-key>\n")
	}

	fmt.Print(b.String())
}

func sectionColor() string {
	return ansiBold + ansiCyan
}

func availableSubcommands(cmd *cobra.Command) []*cobra.Command {
	out := make([]*cobra.Command, 0, len(cmd.Commands()))
	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() || c.Hidden {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func indentBlock(in, prefix string) string {
	lines := strings.Split(strings.TrimSpace(in), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func simpleTitle(v string) string {
	if v == "" {
		return v
	}
	return strings.ToUpper(v[:1]) + v[1:]
}

func prompt(reader *bufio.Reader, key, def string) string {
	fmt.Printf("%s [%s]: ", key, def)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptChoice(reader *bufio.Reader, key, def string, choices []string) string {
	label := fmt.Sprintf("%s (%s)", key, strings.Join(choices, "|"))
	for {
		v := prompt(reader, label, def)
		for _, c := range choices {
			if v == c {
				return v
			}
		}
		fmt.Printf("invalid value for %s: %s\n", key, v)
	}
}

func promptBool(reader *bufio.Reader, key string, def bool) bool {
	d := "false"
	if def {
		d = "true"
	}
	for {
		v := strings.ToLower(prompt(reader, key, d))
		switch v {
		case "true", "t", "1", "yes", "y":
			return true
		case "false", "f", "0", "no", "n":
			return false
		default:
			fmt.Printf("invalid bool for %s: %s\n", key, v)
		}
	}
}

func promptInt(reader *bufio.Reader, key string, def int) int {
	for {
		raw := prompt(reader, key, strconv.Itoa(def))
		v, err := strconv.Atoi(raw)
		if err == nil {
			return v
		}
		fmt.Printf("invalid integer for %s: %s\n", key, raw)
	}
}

func promptCSV(reader *bufio.Reader, key string, def []string) []string {
	joined := strings.Join(def, ",")
	raw := prompt(reader, key, joined)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validateWizardConfig(cfg config.Config) error {
	tmp, err := os.CreateTemp("", "opencortex-config-*.yaml")
	if err != nil {
		return err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	_, err = config.Load(path)
	return err
}
