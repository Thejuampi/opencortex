package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"opencortex/internal/bootstrap"
	"opencortex/internal/broker"
	"opencortex/internal/config"
	"opencortex/internal/service"
	"opencortex/internal/storage"
	"opencortex/internal/storage/repos"
)

type bootstrapRunOptions struct {
	All         bool
	MCPOnly     bool
	VSCodeOnly  bool
	Silent      bool
	NoAutostart bool
	ServerURL   string
	AdminName   string
}

func runBootstrap(ctx context.Context, cfgPath string, cfg config.Config, opts bootstrapRunOptions) (bootstrap.State, error) {
	state, _ := bootstrap.LoadState()
	if !opts.All && !opts.MCPOnly && !opts.VSCodeOnly {
		opts.All = true
	}
	if err := bootstrap.EnsureDir(); err != nil {
		return state, err
	}
	if opts.All {
		db, err := storage.Open(ctx, cfg)
		if err != nil {
			return state, err
		}
		defer db.Close()
		if err := storage.Migrate(ctx, db); err != nil {
			return state, err
		}
		store := repos.New(db)
		memBroker := broker.NewMemory(cfg.Broker.ChannelBufferSize)
		app := service.New(cfg, store, memBroker)
		if strings.TrimSpace(cfg.Auth.AdminKey) == "" {
			name := strings.TrimSpace(opts.AdminName)
			if name == "" {
				name = "admin"
			}
			_, key, err := app.BootstrapInit(ctx, name)
			if err != nil && !errors.Is(err, service.ErrConflict) {
				return state, err
			}
			if err == nil && strings.TrimSpace(key) != "" {
				cfg.Auth.AdminKey = key
				if err := saveConfig(cfgPath, cfg); err != nil {
					return state, err
				}
			}
		} else {
			if err := store.SeedRBAC(ctx); err != nil {
				return state, err
			}
		}
	}

	serverURL := strings.TrimSpace(opts.ServerURL)
	if serverURL == "" {
		serverURL = fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	}
	copilotOK, copilotErr := bootstrap.UpsertCopilotConfig(serverURL)
	codexOK, codexErr := bootstrap.UpsertCodexConfig()
	contextPath, contextErr := bootstrap.EnsureAgentContextSnippet(serverURL)

	vscodeOK := state.VSCodeExtensionInstalled
	if bootstrap.DetectVSCodePresence() {
		vscodeOK = true
	}
	if opts.All || opts.VSCodeOnly {
		installed, err := bootstrap.TryInstallVSCodeExtension(ctx)
		if err != nil && !opts.Silent {
			fmt.Printf("warning: vscode extension install failed: %v\n", err)
		}
		vscodeOK = installed || vscodeOK
	}

	autostartOK := state.AutostartConfigured
	if opts.All && !opts.NoAutostart {
		exePath, _ := os.Executable()
		enabled, err := bootstrap.ConfigureAutostart(exePath, cfgPath)
		if err != nil && !opts.Silent {
			fmt.Printf("warning: autostart setup failed: %v\n", err)
		}
		autostartOK = enabled || autostartOK
	}

	state.Version = bootstrap.StateVersion
	state.BootstrappedAt = time.Now().UTC()
	state.ServerURL = serverURL
	state.Port = cfg.Server.Port
	state.CopilotMCPConfigured = copilotOK || state.CopilotMCPConfigured
	state.CodexMCPConfigured = codexOK || state.CodexMCPConfigured
	state.VSCodeExtensionInstalled = vscodeOK
	state.AutostartConfigured = autostartOK
	state.AgentContextPath = contextPath
	if copilotErr != nil {
		state.LastError = copilotErr.Error()
	} else if codexErr != nil {
		state.LastError = codexErr.Error()
	} else if contextErr != nil {
		state.LastError = contextErr.Error()
	}
	if err := bootstrap.SaveState(state); err != nil {
		return state, err
	}
	if contextErr != nil {
		return state, contextErr
	}
	return state, nil
}

func selectAvailablePort(start, maxAttempts int) (int, error) {
	if start <= 0 {
		start = 8080
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	for i := 0; i < maxAttempts; i++ {
		port := start + i
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port in range %d-%d", start, start+maxAttempts-1)
}

func maybeOpenBrowser(rawURL string) {
	if strings.TrimSpace(rawURL) == "" {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	case "darwin":
		cmd = exec.Command("open", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	_ = cmd.Start()
}

func saveConfig(path string, cfg config.Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
