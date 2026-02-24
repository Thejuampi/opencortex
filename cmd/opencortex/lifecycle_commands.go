package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"opencortex/internal/bootstrap"
	"opencortex/internal/storage"
)

type localStatus struct {
	Running        bool   `json:"running"`
	PID            int    `json:"pid,omitempty"`
	ServerURL      string `json:"server_url,omitempty"`
	Healthy        bool   `json:"healthy"`
	ConfigPath     string `json:"config_path"`
	DBPath         string `json:"db_path"`
	MCPEnabled     bool   `json:"mcp_enabled"`
	MCPHTTP        bool   `json:"mcp_http_enabled"`
	CurrentAuth    string `json:"current_auth,omitempty"`
	CurrentProfile string `json:"current_profile,omitempty"`
	CurrentAgent   string `json:"current_agent,omitempty"`
	LastStartErr   string `json:"last_start_error,omitempty"`
	ConsentCached  *bool  `json:"auto_start_local_server,omitempty"`
}

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

func newStartCommand(cfgPath *string, asJSON *bool) *cobra.Command {
	var yes bool
	var foreground bool
	var openBrowser bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start local OpenCortex runtime",
		RunE: func(cmd *cobra.Command, args []string) error {
			if yes {
				st, _ := loadCLIState()
				v := true
				st.AutoStartLocalServer = &v
				now := time.Now().UTC()
				st.LastPromptedAt = &now
				_ = saveCLIState(st)
			}

			baseURL := discoverServerURL()
			if foreground {
				serverCmd := newServerCommand(cfgPath)
				_ = serverCmd.Flags().Set("open-browser", fmt.Sprintf("%v", openBrowser))
				return serverCmd.RunE(serverCmd, []string{})
			}

			if isServerHealthy(baseURL) {
				if *asJSON {
					return printJSON(map[string]any{"running": true, "server_url": baseURL})
				}
				fmt.Printf("already running at %s\n", baseURL)
				return nil
			}
			if err := startLocalServerDetached(*cfgPath); err != nil {
				return err
			}
			if err := waitForServerReady(baseURL, 15*time.Second); err != nil {
				return err
			}
			if *asJSON {
				return printJSON(map[string]any{"started": true, "server_url": baseURL})
			}
			fmt.Printf("started local server at %s\n", baseURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Persist auto-start consent and skip prompt")
	cmd.Flags().BoolVar(&foreground, "foreground", false, "Run in foreground (same as `server`)")
	cmd.Flags().BoolVar(&openBrowser, "open-browser", true, "Open browser in foreground mode")
	return cmd
}

func newStopCommand(asJSON *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop local OpenCortex runtime",
		RunE: func(cmd *cobra.Command, args []string) error {
			stopped, err := stopLocalServer()
			if err != nil {
				return err
			}
			if *asJSON {
				return printJSON(map[string]any{"stopped": stopped})
			}
			if stopped {
				fmt.Println("stopped local server")
			} else {
				fmt.Println("local server was not running")
			}
			return nil
		},
	}
}

func newStatusCommand(cfgPath *string, asJSON *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local OpenCortex status",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := collectLocalStatus(*cfgPath)
			if err != nil {
				return err
			}
			if *asJSON {
				return printJSON(st)
			}
			fmt.Printf("running: %v\n", st.Running)
			if st.PID > 0 {
				fmt.Printf("pid: %d\n", st.PID)
			}
			fmt.Printf("server_url: %s\n", st.ServerURL)
			fmt.Printf("healthy: %v\n", st.Healthy)
			fmt.Printf("config: %s\n", st.ConfigPath)
			fmt.Printf("db: %s\n", st.DBPath)
			if st.CurrentProfile != "" {
				fmt.Printf("profile: %s\n", st.CurrentProfile)
			}
			if st.CurrentAgent != "" {
				fmt.Printf("auth: %s (%s)\n", st.CurrentAuth, st.CurrentAgent)
			}
			return nil
		},
	}
}

func newDoctorCommand(cfgPath *string, asJSON *bool) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local setup and suggest fixes",
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := runDoctor(*cfgPath, fix)
			if *asJSON {
				return printJSON(map[string]any{"checks": checks})
			}
			for _, c := range checks {
				fmt.Printf("[%s] %s: %s\n", strings.ToUpper(c.Status), c.Name, c.Message)
				if c.Fix != "" {
					fmt.Printf("  fix: %s\n", c.Fix)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Apply safe automatic remediations")
	return cmd
}

func collectLocalStatus(cfgPath string) (localStatus, error) {
	cfg, err := loadConfigMaybe(cfgPath)
	if err != nil {
		return localStatus{}, err
	}
	pid, addr, _ := readLockFile()
	running := pid > 0 && isProcessAlive(pid)
	if !running {
		pid = 0
	}
	serverURL := strings.TrimSpace(addr)
	if serverURL == "" {
		serverURL = discoverServerURL()
	}
	store, _ := authStoreLoad()
	var currentAuth, currentProfile, currentAgent string
	base := canonicalBaseURL(serverURL)
	currentAuth = base
	if p, ok := authStoreCurrentProfile(base); ok || p != "" {
		currentProfile = p
		if acc, ok := store.Accounts[authAccountKey(base, p)]; ok {
			currentAgent = acc.AgentName
		}
	}
	cliSt, _ := loadCLIState()
	return localStatus{
		Running:        running,
		PID:            pid,
		ServerURL:      serverURL,
		Healthy:        isServerHealthy(serverURL),
		ConfigPath:     cfgPath,
		DBPath:         cfg.Database.Path,
		MCPEnabled:     cfg.MCP.Enabled,
		MCPHTTP:        cfg.MCP.HTTP.Enabled,
		CurrentAuth:    currentAuth,
		CurrentProfile: currentProfile,
		CurrentAgent:   currentAgent,
		LastStartErr:   cliSt.LastStartError,
		ConsentCached:  cliSt.AutoStartLocalServer,
	}, nil
}

func runDoctor(cfgPath string, fix bool) []doctorCheck {
	out := []doctorCheck{}

	if _, err := os.Stat(cfgPath); err != nil {
		if fix {
			if _, mkErr := bootstrap.EnsureManagedConfig(cfgPath); mkErr == nil {
				out = append(out, doctorCheck{Name: "config", Status: "ok", Message: "managed config created", Fix: "created missing config"})
			} else {
				out = append(out, doctorCheck{Name: "config", Status: "fail", Message: mkErr.Error(), Fix: "run `opencortex start`"})
			}
		} else {
			out = append(out, doctorCheck{Name: "config", Status: "fail", Message: "config missing", Fix: "run `opencortex start` or `opencortex doctor --fix`"})
		}
	} else {
		out = append(out, doctorCheck{Name: "config", Status: "ok", Message: "config present"})
	}

	cfg, cfgErr := loadConfigMaybe(cfgPath)
	if cfgErr != nil {
		out = append(out, doctorCheck{Name: "config_parse", Status: "fail", Message: cfgErr.Error()})
		return out
	}
	out = append(out, doctorCheck{Name: "config_parse", Status: "ok", Message: "config parsed"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, cfg)
	if err != nil {
		out = append(out, doctorCheck{Name: "database", Status: "fail", Message: err.Error()})
	} else {
		_ = db.Close()
		out = append(out, doctorCheck{Name: "database", Status: "ok", Message: "database openable"})
	}

	baseURL := discoverServerURL()
	if isServerHealthy(baseURL) {
		out = append(out, doctorCheck{Name: "server_health", Status: "ok", Message: "server healthy"})
	} else {
		out = append(out, doctorCheck{Name: "server_health", Status: "warn", Message: "server not reachable", Fix: "run `opencortex start`"})
	}

	if pid, _, err := readLockFile(); err == nil && pid > 0 && !isProcessAlive(pid) {
		if fix {
			_ = os.Remove(lockFilePath())
			out = append(out, doctorCheck{Name: "lock_file", Status: "ok", Message: "stale lock removed", Fix: "removed stale lock"})
		} else {
			out = append(out, doctorCheck{Name: "lock_file", Status: "warn", Message: "stale lock detected", Fix: "run `opencortex doctor --fix`"})
		}
	} else {
		out = append(out, doctorCheck{Name: "lock_file", Status: "ok", Message: "lock file consistent"})
	}

	store, _ := authStoreLoad()
	if profile, ok := authStoreCurrentProfile(baseURL); ok && strings.TrimSpace(profile) != "" {
		if acc, ok := store.Accounts[authAccountKey(baseURL, profile)]; ok && strings.TrimSpace(acc.APIKey) != "" && isServerHealthy(baseURL) {
			client := newAPIClient(baseURL, acc.APIKey)
			var me map[string]any
			if err := client.do(http.MethodGet, "/api/v1/agents/me", nil, &me); err != nil {
				out = append(out, doctorCheck{Name: "auth_token", Status: "warn", Message: "saved token could not be validated", Fix: "run `opencortex auth login`"})
			} else {
				out = append(out, doctorCheck{Name: "auth_token", Status: "ok", Message: "saved token valid"})
			}
		}
	}

	return out
}
