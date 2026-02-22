package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"opencortex/internal/config"
)

var testBinaryPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "opencortex-e2e-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup failed: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	binName := "opencortex"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	testBinaryPath = filepath.Join(tmpDir, binName)

	buildCmd := exec.Command("go", "build", "-o", testBinaryPath, "./cmd/opencortex")
	buildCmd.Dir = repoRoot()
	buildCmd.Env = os.Environ()
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e binary build failed: %v\n%s\n", err, string(out))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

type serverHandle struct {
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan error
	baseURL  string
	cfgPath  string
	homeDir  string
	adminKey string
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
}

type agentIdentity struct {
	Name string
	ID   string
	Key  string
}

type happySummary struct {
	Started                bool
	CreatedAgents          int
	KBPosts                int
	SendOps                int
	InboxAgentsWithMessage int
	StatsAgents            int
	StatsKnowledgeEntries  int
	StatsMessages          int
	MinStatsSatisfied      bool
	ErrorCount             int
}

type teardownSummary struct {
	Started    bool
	CLIReady   bool
	Stopped    bool
	HealthDown bool
	ErrorCount int
}

func TestE2E_HappyPath_ConcurrentAgents(t *testing.T) {
	t.Parallel()

	summary := happySummary{}
	var errs []string
	addErr := func(err error) {
		if err == nil {
			return
		}
		errs = append(errs, err.Error())
		summary.ErrorCount++
	}

	homeDir := t.TempDir()
	workDir := t.TempDir()
	port, err := freePort()
	if err != nil {
		addErr(fmt.Errorf("reserve port: %w", err))
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	env, envErr := isolatedEnv(homeDir, baseURL)
	if envErr != nil {
		addErr(envErr)
	}

	var h *serverHandle
	if summary.ErrorCount == 0 {
		h, err = startServer(homeDir, port, env)
		if err != nil {
			addErr(err)
		} else {
			summary.Started = true
		}
	}

	if h != nil {
		defer func() {
			addErr(stopServer(h, 6*time.Second))
		}()
	}

	agentCount := 4
	agents := make([]agentIdentity, 0, agentCount)
	var mu sync.Mutex

	if h != nil {
		runID := time.Now().UnixNano()
		var wg sync.WaitGroup
		for i := 0; i < agentCount; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				name := fmt.Sprintf("e2e-%d-a%d", runID, i+1)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, _, runErr := runCLIJSON(ctx, env,
					"--base-url", h.baseURL,
					"--config", h.cfgPath,
					"--api-key", h.adminKey,
					"--json",
					"agents", "create",
					"--name", name,
					"--type", "ai",
					"--tags", "e2e,happy",
				)
				mu.Lock()
				defer mu.Unlock()
				if runErr != nil {
					addErr(fmt.Errorf("create agent %s: %w", name, runErr))
					return
				}
				id := nestedString(out, "agent", "id")
				key := nestedString(out, "api_key")
				if id == "" || key == "" {
					addErr(fmt.Errorf("create agent %s missing id/key", name))
					return
				}
				agents = append(agents, agentIdentity{Name: name, ID: id, Key: key})
				summary.CreatedAgents++
			}()
		}
		wg.Wait()
	}

	if len(agents) > 0 {
		var wg sync.WaitGroup
		for i := range agents {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				a := agents[i]
				mdPath := filepath.Join(workDir, fmt.Sprintf("%s.md", a.Name))
				content := fmt.Sprintf("# %s\n\nHappy-path KB entry from %s\n", a.Name, a.Name)
				if writeErr := os.WriteFile(mdPath, []byte(content), 0o600); writeErr != nil {
					mu.Lock()
					addErr(fmt.Errorf("write kb file %s: %w", a.Name, writeErr))
					mu.Unlock()
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _, runErr := runCLI(ctx, env,
					"--base-url", h.baseURL,
					"--config", h.cfgPath,
					"--api-key", a.Key,
					"knowledge", "add",
					"--title", "E2E "+a.Name,
					"--file", mdPath,
					"--summary", "happy path",
					"--tags", "e2e,happy",
				)
				mu.Lock()
				defer mu.Unlock()
				if runErr != nil {
					addErr(fmt.Errorf("knowledge add %s: %w", a.Name, runErr))
					return
				}
				summary.KBPosts++
			}()
		}
		wg.Wait()
	}

	if len(agents) > 0 {
		var wg sync.WaitGroup
		for i := range agents {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				from := agents[i]
				to := agents[(i+1)%len(agents)]
				msg := fmt.Sprintf("e2e-msg-%s-to-%s", from.Name, to.Name)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _, runErr := runCLI(ctx, env,
					"--base-url", h.baseURL,
					"--config", h.cfgPath,
					"--api-key", from.Key,
					"send", "--to", to.Name, msg,
				)
				mu.Lock()
				defer mu.Unlock()
				if runErr != nil {
					addErr(fmt.Errorf("send %s->%s: %w", from.Name, to.Name, runErr))
					return
				}
				summary.SendOps++
			}()
		}
		wg.Wait()
	}

	if len(agents) > 0 {
		var wg sync.WaitGroup
		for i := range agents {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				a := agents[i]
				expected := fmt.Sprintf("e2e-msg-%s-to-%s", agents[(i-1+len(agents))%len(agents)].Name, a.Name)
				found := false
				for attempt := 0; attempt < 12; attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					out, _, runErr := runCLIJSON(ctx, env,
						"--base-url", h.baseURL,
						"--config", h.cfgPath,
						"--api-key", a.Key,
						"--json",
						"inbox",
						"--count", "10",
					)
					cancel()
					if runErr == nil {
						for _, m := range nestedSlice(out, "messages") {
							entry, ok := m.(map[string]any)
							if !ok {
								continue
							}
							if strings.Contains(stringOrEmpty(entry["content"]), expected) {
								found = true
								break
							}
						}
					}
					if found {
						break
					}
					time.Sleep(250 * time.Millisecond)
				}
				mu.Lock()
				defer mu.Unlock()
				if !found {
					addErr(fmt.Errorf("inbox missing expected message for %s", a.Name))
					return
				}
				summary.InboxAgentsWithMessage++
			}()
		}
		wg.Wait()
	}

	if h != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out, _, runErr := runCLIJSON(ctx, env,
			"--base-url", h.baseURL,
			"--config", h.cfgPath,
			"--api-key", h.adminKey,
			"--json",
			"admin", "stats",
		)
		cancel()
		if runErr != nil {
			addErr(fmt.Errorf("admin stats: %w", runErr))
		} else {
			summary.StatsAgents = nestedInt(out, "stats", "agents")
			summary.StatsKnowledgeEntries = nestedInt(out, "stats", "knowledge_entries")
			summary.StatsMessages = nestedInt(out, "stats", "messages")
		}
	}

	summary.MinStatsSatisfied = summary.StatsAgents >= 5 &&
		summary.StatsKnowledgeEntries >= 4 &&
		summary.StatsMessages >= 4

	pass := summary.Started &&
		summary.CreatedAgents == 4 &&
		summary.KBPosts == 4 &&
		summary.SendOps == 4 &&
		summary.InboxAgentsWithMessage == 4 &&
		summary.MinStatsSatisfied &&
		summary.ErrorCount == 0

	if !pass {
		t.Fatalf("happy-path e2e failed: summary=%+v errors=%v", summary, errs)
	}
}

func TestE2E_ServerTeardown_IsClean(t *testing.T) {
	t.Parallel()

	summary := teardownSummary{}
	var errs []string
	addErr := func(err error) {
		if err == nil {
			return
		}
		errs = append(errs, err.Error())
		summary.ErrorCount++
	}

	homeDir := t.TempDir()
	port, err := freePort()
	if err != nil {
		addErr(fmt.Errorf("reserve port: %w", err))
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	env, envErr := isolatedEnv(homeDir, baseURL)
	if envErr != nil {
		addErr(envErr)
	}

	var h *serverHandle
	if summary.ErrorCount == 0 {
		h, err = startServer(homeDir, port, env)
		if err != nil {
			addErr(err)
		} else {
			summary.Started = true
		}
	}

	if h != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_, _, runErr := runCLI(ctx, env,
			"--base-url", h.baseURL,
			"--config", h.cfgPath,
			"--api-key", h.adminKey,
			"agents", "list",
		)
		cancel()
		if runErr != nil {
			addErr(fmt.Errorf("agents list before teardown: %w", runErr))
		} else {
			summary.CLIReady = true
		}
		stopErr := stopServer(h, 6*time.Second)
		if stopErr != nil {
			addErr(stopErr)
		} else {
			summary.Stopped = true
		}
	}

	summary.HealthDown = waitForHealthDown(baseURL, 4*time.Second)
	if !summary.HealthDown {
		addErr(errors.New("health endpoint still reachable after teardown"))
	}

	pass := summary.Started &&
		summary.CLIReady &&
		summary.Stopped &&
		summary.HealthDown &&
		summary.ErrorCount == 0

	if !pass {
		t.Fatalf("teardown e2e failed: summary=%+v errors=%v", summary, errs)
	}
}

func startServer(homeDir string, port int, env []string) (*serverHandle, error) {
	cfgPath, err := writeIsolatedConfig(homeDir, port)
	if err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, testBinaryPath,
		"--config", cfgPath,
		"server",
		"--open-browser=false",
		"--no-autostart",
	)
	cmd.Env = env
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start server: %w", err)
	}

	h := &serverHandle{
		cmd:     cmd,
		cancel:  cancel,
		done:    make(chan error, 1),
		baseURL: baseURL,
		cfgPath: cfgPath,
		homeDir: homeDir,
		stdout:  stdout,
		stderr:  stderr,
	}
	go func() {
		h.done <- cmd.Wait()
	}()

	adminKey, waitErr := waitForReady(h, env, 25*time.Second)
	if waitErr != nil {
		_ = stopServer(h, 3*time.Second)
		return nil, waitErr
	}
	h.adminKey = adminKey
	return h, nil
}

func stopServer(h *serverHandle, timeout time.Duration) error {
	if h == nil {
		return nil
	}
	h.cancel()
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(os.Interrupt)
	}

	waitAndClassify := func(err error) error {
		if err == nil {
			return nil
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "killed") || strings.Contains(msg, "signal") || strings.Contains(msg, "interrupted") || strings.Contains(msg, "exit status") {
			return nil
		}
		return err
	}

	select {
	case err := <-h.done:
		return waitAndClassify(err)
	case <-time.After(timeout):
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		select {
		case err := <-h.done:
			return waitAndClassify(err)
		case <-time.After(2 * time.Second):
			return errors.New("server did not exit after kill")
		}
	}
}

func waitForReady(h *serverHandle, env []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		select {
		case err := <-h.done:
			return "", fmt.Errorf("server exited early: %v stderr=%s stdout=%s", err, h.stderr.String(), h.stdout.String())
		default:
		}

		adminKey, _ := readAdminKey(h.cfgPath)
		if adminKey != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _, runErr := runCLI(ctx, env,
				"--base-url", h.baseURL,
				"--config", h.cfgPath,
				"--api-key", adminKey,
				"agents", "list",
			)
			cancel()
			if runErr == nil {
				return adminKey, nil
			}
			lastErr = runErr
		}
		time.Sleep(250 * time.Millisecond)
	}

	return "", fmt.Errorf("timeout waiting for readiness: %v stderr=%s stdout=%s", lastErr, h.stderr.String(), h.stdout.String())
}

func runCLI(ctx context.Context, env []string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, testBinaryPath, args...)
	cmd.Env = env
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func runCLIJSON(ctx context.Context, env []string, args ...string) (map[string]any, string, error) {
	stdout, stderr, err := runCLI(ctx, env, args...)
	if err != nil {
		return nil, stderr, err
	}
	var out map[string]any
	if uErr := json.Unmarshal([]byte(stdout), &out); uErr != nil {
		return nil, stderr, fmt.Errorf("decode json output: %w stdout=%s", uErr, stdout)
	}
	return out, stderr, nil
}

func isolatedEnv(homeDir, baseURL string) ([]string, error) {
	envMap := map[string]string{}
	for _, kv := range os.Environ() {
		if idx := strings.Index(kv, "="); idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}

	appData := filepath.Join(homeDir, "AppData", "Roaming")
	localAppData := filepath.Join(homeDir, "AppData", "Local")
	xdgConfig := filepath.Join(homeDir, ".config")
	if err := os.MkdirAll(appData, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(localAppData, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(xdgConfig, 0o755); err != nil {
		return nil, err
	}

	envMap["HOME"] = homeDir
	envMap["USERPROFILE"] = homeDir
	envMap["APPDATA"] = appData
	envMap["LOCALAPPDATA"] = localAppData
	envMap["XDG_CONFIG_HOME"] = xdgConfig
	envMap["OPENCORTEX_URL"] = baseURL

	out := make([]string, 0, len(envMap))
	for k, v := range envMap {
		out = append(out, k+"="+v)
	}
	return out, nil
}

func writeIsolatedConfig(homeDir string, port int) (string, error) {
	ocDir := filepath.Join(homeDir, ".opencortex")
	if err := os.MkdirAll(ocDir, 0o700); err != nil {
		return "", err
	}
	cfgPath := filepath.Join(ocDir, "config.yaml")
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = port
	cfg.Database.Path = filepath.Join(ocDir, "data.db")
	cfg.Database.BackupPath = filepath.Join(ocDir, "backups")
	cfg.Logging.File = filepath.Join(ocDir, "opencortex.log")
	cfg.Auth.Enabled = true
	cfg.Auth.AdminKey = ""
	cfg.Sync.Enabled = false
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		return "", err
	}
	return cfgPath, nil
}

func readAdminKey(cfgPath string) (string, error) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`(?m)^\s*admin_key:\s*(\S+)\s*$`)
	m := re.FindStringSubmatch(string(b))
	if len(m) != 2 {
		return "", nil
	}
	v := strings.TrimSpace(m[1])
	v = strings.Trim(v, `"'`)
	if strings.EqualFold(v, "null") {
		return "", nil
	}
	return v, nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("unexpected listener addr type")
	}
	return addr.Port, nil
}

func waitForHealthDown(baseURL string, timeout time.Duration) bool {
	client := &http.Client{Timeout: 350 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/healthz")
		if err != nil {
			return true
		}
		io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		time.Sleep(120 * time.Millisecond)
	}
	return false
}

func nestedString(m map[string]any, path ...string) string {
	var current any = m
	for _, p := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[p]
	}
	return stringOrEmpty(current)
}

func nestedInt(m map[string]any, path ...string) int {
	var current any = m
	for _, p := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = obj[p]
	}
	switch v := current.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

func nestedSlice(m map[string]any, path ...string) []any {
	var current any = m
	for _, p := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = obj[p]
	}
	v, _ := current.([]any)
	return v
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
