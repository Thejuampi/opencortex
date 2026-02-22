package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"opencortex/internal/config"
)

const (
	StateVersion      = 1
	CodexBlockStart   = "# BEGIN OPENCORTEX MCP"
	CodexBlockEnd     = "# END OPENCORTEX MCP"
	VSCodeExtensionID = "opencortex.opencortex-vscode"
)

type State struct {
	Version                  int       `json:"version"`
	BootstrappedAt           time.Time `json:"bootstrapped_at"`
	ServerURL                string    `json:"server_url"`
	Port                     int       `json:"port"`
	CopilotMCPConfigured     bool      `json:"copilot_mcp_configured"`
	CodexMCPConfigured       bool      `json:"codex_mcp_configured"`
	VSCodeExtensionInstalled bool      `json:"vscode_extension_installed"`
	AutostartConfigured      bool      `json:"autostart_configured"`
	AgentContextPath         string    `json:"agent_context_path"`
	LastError                string    `json:"last_error,omitempty"`
}

type Status struct {
	ConfigPath               string `json:"config_path"`
	DBPath                   string `json:"db_path"`
	StatePath                string `json:"state_path"`
	ServerURL                string `json:"server_url"`
	Port                     int    `json:"port"`
	ConfigPresent            bool   `json:"config_present"`
	DBPresent                bool   `json:"db_present"`
	CopilotMCPConfigured     bool   `json:"copilot_mcp_configured"`
	CodexMCPConfigured       bool   `json:"codex_mcp_configured"`
	VSCodeExtensionInstalled bool   `json:"vscode_extension_installed"`
	AutostartConfigured      bool   `json:"autostart_configured"`
	AgentContextPath         string `json:"agent_context_path"`
}

func HomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return os.TempDir()
	}
	return home
}

func Dir() string {
	return filepath.Join(HomeDir(), ".opencortex")
}

func EnsureDir() error {
	return os.MkdirAll(Dir(), 0o700)
}

func ManagedConfigPath() string {
	return filepath.Join(Dir(), "config.yaml")
}

func ManagedDBPath() string {
	return filepath.Join(Dir(), "data.db")
}

func StatePath() string {
	return filepath.Join(Dir(), "state.json")
}

func ServerPath() string {
	return filepath.Join(Dir(), "server")
}

func AgentContextPath() string {
	return filepath.Join(Dir(), "agent-bootstrap.md")
}

func EnsureManagedConfig(path string) (config.Config, error) {
	if err := EnsureDir(); err != nil {
		return config.Config{}, err
	}
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 8080
	cfg.Database.Path = ManagedDBPath()
	cfg.Database.BackupPath = filepath.Join(Dir(), "backups")
	cfg.Logging.File = filepath.Join(Dir(), "opencortex.log")
	if _, err := os.Stat(path); err == nil {
		return config.Load(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.Config{}, err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return config.Config{}, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func LoadState() (State, error) {
	b, err := os.ReadFile(StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{Version: StateVersion}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(b, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func SaveState(state State) error {
	if err := EnsureDir(); err != nil {
		return err
	}
	state.Version = StateVersion
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(StatePath(), b, 0o600)
}

func UpsertCopilotConfig(serverURL string) (bool, error) {
	path, err := copilotConfigPath()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	raw := []byte("{}")
	if b, readErr := os.ReadFile(path); readErr == nil && len(strings.TrimSpace(string(b))) > 0 {
		raw = b
	}
	merged, changed, err := mergeCopilotMCP(raw, serverURL)
	if err != nil {
		return false, err
	}
	if !changed {
		return true, nil
	}
	if err := os.WriteFile(path, merged, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func UpsertCodexConfig() (bool, error) {
	path := filepath.Join(HomeDir(), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	current := ""
	if b, err := os.ReadFile(path); err == nil {
		current = string(b)
	}
	merged, changed := mergeCodexConfig(current)
	if !changed {
		return true, nil
	}
	if err := os.WriteFile(path, []byte(merged), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func EnsureAgentContextSnippet(serverURL string) (string, error) {
	if err := EnsureDir(); err != nil {
		return "", err
	}
	path := AgentContextPath()
	content := strings.TrimSpace(fmt.Sprintf(`
# OpenCortex Agent Bootstrap

Tell your agent to run this:

curl -fsSL https://thejuampi.github.io/opencortex/install | sh

Local server URL: %s
`, serverURL)) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func TryInstallVSCodeExtension(ctx context.Context) (bool, error) {
	codePath, err := findVSCodeCLI()
	if err != nil {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, codePath, "--install-extension", VSCodeExtensionID, "--force")
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return false, fmt.Errorf("install vscode extension: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return true, nil
}

func DetectVSCodePresence() bool {
	if strings.TrimSpace(os.Getenv("VSCODE_IPC_HOOK_CLI")) != "" {
		return true
	}
	if _, err := findVSCodeCLI(); err == nil {
		return true
	}
	home := HomeDir()
	switch runtime.GOOS {
	case "darwin":
		return fileExists("/Applications/Visual Studio Code.app") ||
			fileExists("/Applications/Visual Studio Code - Insiders.app") ||
			fileExists(filepath.Join(home, "Applications", "Visual Studio Code.app")) ||
			fileExists(filepath.Join(home, "Applications", "Visual Studio Code - Insiders.app")) ||
			fileExists(filepath.Join(home, ".vscode"))
	case "linux":
		return fileExists(filepath.Join(home, ".config", "Code")) ||
			fileExists(filepath.Join(home, ".config", "Code - Insiders")) ||
			fileExists(filepath.Join(home, ".vscode"))
	case "windows":
		localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		return fileExists(filepath.Join(localAppData, "Programs", "Microsoft VS Code", "Code.exe")) ||
			fileExists(filepath.Join(localAppData, "Programs", "Microsoft VS Code Insiders", "Code - Insiders.exe")) ||
			fileExists(filepath.Join(localAppData, "Programs", "VSCodium", "VSCodium.exe")) ||
			fileExists(filepath.Join(appData, "Code")) ||
			fileExists(filepath.Join(appData, "Code - Insiders")) ||
			fileExists(filepath.Join(home, ".vscode"))
	default:
		return false
	}
}

func findVSCodeCLI() (string, error) {
	candidates := []string{"code", "code.cmd", "code-insiders", "code-insiders.cmd", "codium", "codium.cmd"}
	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "windows" {
		localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		paths := []string{
			filepath.Join(localAppData, "Programs", "Microsoft VS Code", "bin", "code.cmd"),
			filepath.Join(localAppData, "Programs", "Microsoft VS Code Insiders", "bin", "code-insiders.cmd"),
			filepath.Join(localAppData, "Programs", "VSCodium", "bin", "codium.cmd"),
		}
		for _, p := range paths {
			if fileExists(p) {
				return p, nil
			}
		}
	}
	return "", errors.New("vscode cli not found")
}

func CurrentStatus() Status {
	state, _ := LoadState()
	configPath := ManagedConfigPath()
	cfgPresent := fileExists(configPath)
	dbPresent := fileExists(ManagedDBPath())
	vscodeDetected := DetectVSCodePresence()
	status := Status{
		ConfigPath:               configPath,
		DBPath:                   ManagedDBPath(),
		StatePath:                StatePath(),
		ServerURL:                strings.TrimSpace(state.ServerURL),
		Port:                     state.Port,
		ConfigPresent:            cfgPresent,
		DBPresent:                dbPresent,
		CopilotMCPConfigured:     state.CopilotMCPConfigured,
		CodexMCPConfigured:       state.CodexMCPConfigured,
		VSCodeExtensionInstalled: state.VSCodeExtensionInstalled || vscodeDetected,
		AutostartConfigured:      state.AutostartConfigured,
		AgentContextPath:         state.AgentContextPath,
	}
	if status.ServerURL == "" {
		if b, err := os.ReadFile(ServerPath()); err == nil {
			status.ServerURL = strings.TrimSpace(string(b))
		}
	}
	return status
}

func mergeCopilotMCP(raw []byte, serverURL string) ([]byte, bool, error) {
	var cfg map[string]any
	if len(strings.TrimSpace(string(raw))) == 0 {
		cfg = map[string]any{}
	} else if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false, err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		servers = map[string]any{}
		cfg["mcpServers"] = servers
	}
	next := map[string]any{
		"command": "opencortex",
		"args":    []string{"mcp"},
		"env": map[string]any{
			"OPENCORTEX_URL": strings.TrimSpace(serverURL),
		},
	}
	changed := true
	if existing, ok := servers["opencortex"]; ok {
		if sameJSON(existing, next) {
			changed = false
		}
	}
	servers["opencortex"] = next
	merged, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(merged, '\n'), changed, nil
}

func mergeCodexConfig(current string) (string, bool) {
	block := strings.TrimSpace(`
# BEGIN OPENCORTEX MCP
[mcp_servers.opencortex]
command = "opencortex"
args = ["mcp"]
# END OPENCORTEX MCP
`) + "\n"

	if strings.Contains(current, CodexBlockStart) && strings.Contains(current, CodexBlockEnd) {
		start := strings.Index(current, CodexBlockStart)
		end := strings.Index(current, CodexBlockEnd)
		if start >= 0 && end >= start {
			end += len(CodexBlockEnd)
			next := strings.TrimSpace(current[:start]) + "\n\n" + block
			if tail := strings.TrimSpace(current[end:]); tail != "" {
				next = strings.TrimSpace(next) + "\n\n" + tail + "\n"
			}
			return next, next != current
		}
	}
	if strings.Contains(current, "[mcp_servers.opencortex]") {
		return current, false
	}
	base := strings.TrimSpace(current)
	if base == "" {
		return block, true
	}
	return base + "\n\n" + block, true
}

func copilotConfigPath() (string, error) {
	home := HomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "GitHub Copilot", "mcp.json"), nil
	case "linux":
		return filepath.Join(home, ".config", "github", "copilot", "mcp.json"), nil
	case "windows":
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(appData, "GitHub Copilot", "mcp.json"), nil
	default:
		return "", fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

func sameJSON(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ab) == string(bb)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
