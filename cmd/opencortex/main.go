package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"opencortex/internal/api"
	"opencortex/internal/api/handlers"
	ws "opencortex/internal/api/websocket"
	"opencortex/internal/bootstrap"
	"opencortex/internal/broker"
	"opencortex/internal/config"
	mcpbridge "opencortex/internal/mcp"
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

const defaultAgentProfile = "default"

var (
	agentProfileFlag    string
	profileNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	errInvalidProfileID = errors.New("invalid agent profile")
)

func normalizeProfileName(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	if p == "" {
		return defaultAgentProfile, nil
	}
	if !profileNamePattern.MatchString(p) {
		return "", fmt.Errorf("%w: %q (allowed: [a-z0-9][a-z0-9._-]{0,63})", errInvalidProfileID, raw)
	}
	return p, nil
}

func resolveProfile(baseURL, flagProfile string) (string, string, error) {
	if p := strings.TrimSpace(flagProfile); p != "" {
		n, err := normalizeProfileName(p)
		return n, "flag", err
	}
	if p := strings.TrimSpace(os.Getenv("OPENCORTEX_AGENT_PROFILE")); p != "" {
		n, err := normalizeProfileName(p)
		return n, "env", err
	}
	if p, ok := authStoreCurrentProfile(baseURL); ok {
		n, err := normalizeProfileName(p)
		return n, "store", err
	}
	return defaultAgentProfile, "default", nil
}

func newAPIClient(baseURL, apiKey string) *apiClient {
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	baseURL = canonicalBaseURL(baseURL)
	if strings.TrimSpace(apiKey) == "" {
		profile, _, _ := resolveProfile(baseURL, agentProfileFlag)
		if saved, ok := authStoreGetTokenForProfile(baseURL, profile); ok {
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
		cfgPath      string
		baseURL      string
		apiKey       string
		asJSON       bool
		agentProfile string
	)

	root := &cobra.Command{
		Use:   "opencortex",
		Short: "Opencortex self-hosted multi-agent infrastructure node",
		Long:  "Single-binary platform for multi-agent messaging, knowledge management, and selective sync.",
		Example: strings.TrimSpace(`
  opencortex server                          # start once
  opencortex send --to researcher "analyse src/auth.go"
  opencortex inbox --wait
  opencortex broadcast "deploying v2.1"
  opencortex knowledge search "auth patterns"
  opencortex skills --help
  opencortex agents`),
	}
	root.SetHelpFunc(renderHelp)
	root.PersistentFlags().StringVar(&cfgPath, "config", bootstrap.ManagedConfigPath(), "Path to config file")
	root.PersistentFlags().StringVar(&baseURL, "base-url", discoverServerURL(), "Base API URL for CLI commands")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for CLI commands (optional on localhost)")
	root.PersistentFlags().StringVar(&agentProfile, "agent-profile", "", "Agent profile identity for multi-agent local usage")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Print JSON output")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		agentProfileFlag = agentProfile
		_, _, err := resolveProfile(baseURL, agentProfileFlag)
		return err
	}

	root.AddCommand(newInitCommand(&cfgPath))
	root.AddCommand(newServerCommand(&cfgPath))
	root.AddCommand(newMCPServerCommand(&cfgPath))
	root.AddCommand(newMCPCommand(&cfgPath)) // 'mcp' alias, no --api-key needed
	root.AddCommand(newConfigCommand(&cfgPath))
	root.AddCommand(newAuthCommand(&baseURL, &apiKey, &agentProfile, &asJSON))
	root.AddCommand(newStartCommand(&cfgPath, &asJSON))
	root.AddCommand(newStopCommand(&asJSON))
	root.AddCommand(newStatusCommand(&cfgPath, &asJSON))
	root.AddCommand(newDoctorCommand(&cfgPath, &asJSON))
	root.AddCommand(newAgentsCommand(&cfgPath, &baseURL, &apiKey, &asJSON))
	root.AddCommand(newSendCommand(&cfgPath, &baseURL, &apiKey))
	root.AddCommand(newInboxCommand(&cfgPath, &baseURL, &apiKey, &asJSON))
	root.AddCommand(newAckCommand(&baseURL, &apiKey))
	root.AddCommand(newWorkerCommand(&cfgPath, &baseURL, &apiKey))
	root.AddCommand(newWatchCommand(&cfgPath, &baseURL, &apiKey))
	root.AddCommand(newBroadcastCommand(&cfgPath, &baseURL, &apiKey))
	root.AddCommand(newKnowledgeCommand(&baseURL, &apiKey, &asJSON))
	root.AddCommand(newSkillsCommand(&cfgPath, &baseURL, &apiKey, &asJSON))
	root.AddCommand(newSyncCommand(&cfgPath, &baseURL, &apiKey, &asJSON))
	root.AddCommand(newAdminCommand(&cfgPath, &baseURL, &apiKey, &asJSON))

	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

// ─── Zero-Ceremony Helpers ───────────────────────────────────────────────────

// opencortexDir returns ~/.opencortex (created on demand).
func opencortexDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	dir := filepath.Join(home, ".opencortex")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// serverFilePath is the file the server writes its URL to on start.
func serverFilePath() string { return filepath.Join(opencortexDir(), "server") }

// lockFilePath is the PID lock file used to prevent duplicate servers.
func lockFilePath() string { return filepath.Join(opencortexDir(), "opencortex.lock") }

// discoverServerURL returns the server URL using the lookup chain:
//  1. OPENCORTEX_URL env var
//  2. ~/.opencortex/server file
//  3. http://localhost:8080 (default)
func discoverServerURL() string {
	if v := strings.TrimSpace(os.Getenv("OPENCORTEX_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if b, err := os.ReadFile(serverFilePath()); err == nil {
		if u := strings.TrimSpace(string(b)); u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return "http://localhost:8080"
}

// writeServerFile writes the server URL to ~/.opencortex/server.
func writeServerFile(serverURL string) {
	_ = os.WriteFile(serverFilePath(), []byte(serverURL+"\n"), 0o600)
}

// removeServerFile removes the server URL file on shutdown.
func removeServerFile() { _ = os.Remove(serverFilePath()) }

// acquireServerLock creates a PID lock file. Returns an error (errAlreadyRunning)
// if another opencortex server is already running.
var errAlreadyRunning = errors.New("already running")

func acquireServerLock(port int) (pid int, addr string, err error) {
	path := lockFilePath()
	if data, readErr := os.ReadFile(path); readErr == nil {
		// File exists — check if PID is alive.
		parts := strings.SplitN(strings.TrimSpace(string(data)), " ", 2)
		if len(parts) >= 1 {
			if existingPID, parseErr := strconv.Atoi(parts[0]); parseErr == nil && existingPID > 0 {
				if isProcessAlive(existingPID) {
					listenAddr := fmt.Sprintf("http://localhost:%d", port)
					if len(parts) >= 2 {
						listenAddr = strings.TrimSpace(parts[1])
					}
					return existingPID, listenAddr, errAlreadyRunning
				}
			}
		}
		// Stale lock — remove it.
		_ = os.Remove(path)
	}
	addr = fmt.Sprintf("http://localhost:%d", port)
	content := fmt.Sprintf("%d %s\n", os.Getpid(), addr)
	return os.Getpid(), addr, os.WriteFile(path, []byte(content), 0o600)
}

// releaseServerLock removes the PID lock file.
func releaseServerLock() { _ = os.Remove(lockFilePath()) }

// isProcessAlive returns true if a process with the given PID is running.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// On Windows os.FindProcess always succeeds; we use tasklist as a fallback.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Try sending signal 0 (POSIX); on Windows we use the exec-based check below.
	if sigErr := proc.Signal(syscall.Signal(0)); sigErr == nil {
		return true
	}
	// Windows fallback: check if PID is in tasklist.
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// localFingerprint builds a stable agent identity for the current CLI process.
// hash(hostname + exe-name + base-url + profile), intentionally excluding PID/session.
func localFingerprint(baseURL, profile string) string {
	hostname, _ := os.Hostname()
	exe, _ := os.Executable()
	exe = filepath.Base(exe)
	baseURL = canonicalBaseURL(baseURL)
	if profile == "" {
		profile = defaultAgentProfile
	}
	h := sha256.Sum256([]byte(hostname + ":" + exe + ":" + baseURL + ":" + profile))
	return "cli-" + hex.EncodeToString(h[:8])
}

// ensureLocalAuth returns a valid API key for baseURL.
// If no key is saved and the server is local, it auto-registers.
func ensureLocalAuth(baseURL string) string {
	profile, _, _ := resolveProfile(baseURL, agentProfileFlag)
	if saved, ok := authStoreGetTokenForProfile(baseURL, profile); ok {
		return saved
	}
	// Only auto-register for localhost URLs.
	if !isLocalURL(baseURL) {
		return ""
	}
	fp := localFingerprint(baseURL, profile)
	hostname, _ := os.Hostname()
	agentName := "cli@" + hostname + ":" + profile

	body, err := json.Marshal(map[string]string{
		"name":        agentName,
		"fingerprint": fp,
	})
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(
		strings.TrimRight(baseURL, "/")+"/api/v1/agents/auto-register",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			APIKey string      `json:"api_key"`
			Agent  model.Agent `json:"agent"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK {
		return ""
	}
	key := out.Data.APIKey
	if key == "" {
		return ""
	}
	_ = authStoreUpsertWithProfile(baseURL, profile, key, map[string]any{
		"id":   out.Data.Agent.ID,
		"name": out.Data.Agent.Name,
	}, []string{"agent"})
	return key
}

// newAutoClient creates an API client, auto-registering on localhost if needed.
func newAutoClient(baseURL, apiKey string) *apiClient {
	if strings.TrimSpace(apiKey) == "" {
		apiKey = ensureLocalAuth(baseURL)
	}
	return newAPIClient(baseURL, apiKey)
}

// isLocalURL returns true if the URL points to the local machine.
func isLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// resolveAgentByName queries the agents list and resolves a name to a single agent ID.
// Returns error if zero or multiple agents match.
func resolveAgentByName(client *apiClient, name string) (string, error) {
	var out struct {
		Agents []model.Agent `json:"agents"`
	}
	if err := client.do(http.MethodGet, "/api/v1/agents?q="+url.QueryEscape(name)+"&per_page=10", nil, &out); err != nil {
		return "", err
	}
	// Filter exact or prefix matches.
	var matches []model.Agent
	lower := strings.ToLower(name)
	for _, a := range out.Agents {
		if strings.Contains(strings.ToLower(a.Name), lower) {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no agent matching %q — run 'opencortex agents' to list available agents", name)
	case 1:
		return matches[0].ID, nil
	default:
		names := make([]string, len(matches))
		for i, a := range matches {
			names[i] = a.Name
		}
		return "", fmt.Errorf("ambiguous: found %s. Be more specific", strings.Join(names, ", "))
	}
}

// ─── New Zero-Ceremony Commands ───────────────────────────────────────────────

// newAgentsShortCommand is a top-level `opencortex agents` alias that lists
// agents without needing any flags — auto-registers on localhost.
func newAgentsShortCommand(baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List available agents (zero-config)",
		Long:  "Lists all active agents. Auto-registers this CLI on localhost if needed.",
		Example: strings.TrimSpace(`
  opencortex agents
  opencortex agents --json`),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAutoClient(*baseURL, *apiKey)
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
	}
}

// newSendCommand implements `opencortex send --to <name> <message>` and
// `opencortex send --topic <id> <message>`.
func newSendCommand(cfgPath, baseURL, apiKey *string) *cobra.Command {
	var (
		toAgent string
		toTopic string
		replyTo bool
	)
	cmd := &cobra.Command{
		Use:   "send <message>",
		Short: "Send a message to an agent or topic",
		Long:  "Send a message without needing an API key (auto-registers on localhost).",
		Args:  cobra.ExactArgs(1),
		Example: strings.TrimSpace(`
  opencortex send --to researcher "Please analyse src/auth.go"
  opencortex send --topic tasks.review "Review PR #142"
  opencortex send --to codex@machine-2 "Deploy to staging" --reply-to-me`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toAgent == "" && toTopic == "" {
				return errors.New("one of --to or --topic is required")
			}
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			body := map[string]any{
				"content":      args[0],
				"content_type": "text/plain",
				"priority":     "normal",
			}
			if toTopic != "" {
				body["topic_id"] = toTopic
			}
			if toAgent != "" {
				agentID, err := resolveAgentByName(client, toAgent)
				if err != nil {
					return err
				}
				body["to_agent_id"] = agentID
			}
			if replyTo {
				body["metadata"] = map[string]any{"reply_to_me": true}
			}
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/messages", body, &out); err != nil {
				return err
			}
			if msg, ok := out["message"].(map[string]any); ok {
				fmt.Printf("sent message %s\n", msg["id"])
			} else {
				fmt.Println("sent")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&toAgent, "to", "", "Recipient agent name or partial name")
	cmd.Flags().StringVar(&toTopic, "topic", "", "Topic ID to publish to")
	cmd.Flags().BoolVar(&replyTo, "reply-to-me", false, "Request a reply back to this agent")
	return cmd
}

// newInboxCommand implements `opencortex inbox [--wait] [--ack]`.
func newInboxCommand(cfgPath, baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	var (
		waitStr  string
		count    int
		all      bool
		priority string
		from     string
		topic    string
		dead     bool
	)
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Read messages in your inbox",
		Long:  "Reads pending messages. Use --wait to long-poll for new messages.",
		Example: strings.TrimSpace(`
  opencortex inbox
  opencortex inbox --wait 30s
  opencortex inbox --priority critical`),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}

			query := url.Values{}
			if count > 0 {
				query.Set("limit", strconv.Itoa(count))
			}
			if all {
				query.Set("all", "true")
			}
			if priority != "" {
				query.Set("priority", priority)
			}
			if from != "" {
				query.Set("from_agent_id", from)
			}
			if topic != "" {
				query.Set("topic_id", topic)
			}
			if dead {
				query.Set("dead", "true")
			}

			if waitStr != "" {
				d, err := time.ParseDuration(waitStr)
				if err != nil {
					return fmt.Errorf("invalid wait duration: %w", err)
				}
				query.Set("wait", strconv.Itoa(int(d.Seconds())))
			}

			var out struct {
				Messages []map[string]any `json:"messages"`
				Cursor   string           `json:"cursor"`
			}
			path := "/api/v1/messages/inbox?" + query.Encode()
			if err := client.do(http.MethodGet, path, nil, &out); err != nil {
				return err
			}
			if *asJSON {
				return printJSON(out)
			}
			if len(out.Messages) == 0 {
				fmt.Println("inbox empty")
				return nil
			}
			for _, m := range out.Messages {
				printInboxMessage(m)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&waitStr, "wait", "", "Block until a message arrives (e.g. '30s')")
	cmd.Flags().IntVar(&count, "count", 50, "Max messages to fetch")
	cmd.Flags().BoolVar(&all, "all", false, "Include read messages")
	cmd.Flags().StringVar(&priority, "priority", "", "Filter by priority")
	cmd.Flags().StringVar(&from, "from", "", "Filter by sender agent ID")
	cmd.Flags().StringVar(&topic, "topic", "", "Filter by topic ID")
	cmd.Flags().BoolVar(&dead, "dead", false, "Include dead-lettered messages")
	return cmd
}

func newAckCommand(baseURL, apiKey *string) *cobra.Command {
	var upTo string
	cmd := &cobra.Command{
		Use:   "ack [msgID...]",
		Short: "Acknowledge messages",
		Long:  "Marks messages as read (acked) and moves the inbox cursor.",
		Example: strings.TrimSpace(`
  opencortex ack msg_xxxx
  opencortex ack --up-to msg_yyyy`),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAutoClient(*baseURL, *apiKey)
			if len(args) == 0 && upTo == "" {
				return errors.New("must provide message IDs or --up-to")
			}

			body := map[string]any{
				"ids": args,
			}
			if upTo != "" {
				body["up_to"] = upTo
			}
			var out struct {
				Acked int64 `json:"acked"`
			}
			if err := client.do(http.MethodPost, "/api/v1/messages/ack", body, &out); err != nil {
				return err
			}
			fmt.Printf("acked %d messages\n", out.Acked)
			return nil
		},
	}
	cmd.Flags().StringVar(&upTo, "up-to", "", "Ack all messages up to and including this ID")
	return cmd
}

func newWorkerCommand(cfgPath, baseURL, apiKey *string) *cobra.Command {
	var (
		topic   string
		group   string
		autoAck bool
	)
	cmd := &cobra.Command{
		Use:   "worker <command> [args...]",
		Short: "Run a command for each message in a blocking loop",
		Args:  cobra.MinimumNArgs(1),
		Example: strings.TrimSpace(`
  opencortex worker my-script.sh
  opencortex worker --topic tasks.review python handler.py`),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}

			exe := args[0]
			cmdArgs := args[1:]

			query := url.Values{}
			query.Set("wait", "30") // default block
			query.Set("limit", "10")
			if topic != "" {
				query.Set("topic_id", topic)
			}
			// group filtering is implicitly handled if group delivers direct messages, but wait, queue mode unassigned?
			// The inbox query processes queue messages automatically for the agent if they are members of the to_group.

			fmt.Printf("Worker started via %s. Waiting for messages...\n", discoverServerURL())
			for {
				var out struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := client.do(http.MethodGet, "/api/v1/messages/inbox?"+query.Encode(), nil, &out); err != nil {
					fmt.Fprintf(os.Stderr, "poll error: %v\n", err)
					time.Sleep(5 * time.Second)
					continue
				}

				for _, m := range out.Messages {
					msgID, _ := m["id"].(string)
					b, _ := json.Marshal(m)

					c := exec.Command(exe, cmdArgs...)
					c.Env = append(os.Environ(), "OPENCORTEX_MESSAGE="+string(b))
					c.Stdout = os.Stdout
					c.Stderr = os.Stderr
					c.Stdin = os.Stdin

					fmt.Printf("Processing %s...\n", msgID)
					err := c.Run()

					if autoAck {
						if err == nil {
							var ackOut map[string]any
							_ = client.do(http.MethodPost, "/api/v1/messages/ack", map[string]any{"ids": []string{msgID}}, &ackOut)
							fmt.Printf("Acked %s\n", msgID)
						} else {
							fmt.Fprintf(os.Stderr, "Worker command failed for %s (no ack).\n", msgID)
						}
					}
				}
			}
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "Filter inbox by topic")
	cmd.Flags().StringVar(&group, "group", "", "Filter inbox by group (future)")
	cmd.Flags().BoolVar(&autoAck, "auto-ack", true, "Auto-ack message if command exits with 0")
	return cmd
}

func printInboxMessage(m map[string]any) {
	id, _ := m["id"].(string)
	from, _ := m["from_agent_id"].(string)
	content, _ := m["content"].(string)
	priority, _ := m["priority"].(string)
	fmt.Printf("[%s] from=%s priority=%s\n  %s\n", id, from, priority, content)
}

// newWatchCommand implements `opencortex watch <topic> — streams messages.
func newWatchCommand(cfgPath, baseURL, apiKey *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <topic>",
		Short: "Watch a topic for new messages",
		Long:  "Polls the topic and prints messages as they arrive. Press Ctrl-C to stop.",
		Args:  cobra.ExactArgs(1),
		Example: strings.TrimSpace(`
  opencortex watch tasks.review
  opencortex watch system.broadcast`),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			topicID := args[0]
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			fmt.Printf("watching topic %q — Ctrl-C to stop\n", topicID)
			seen := map[string]bool{}
			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}
				var out struct {
					Messages []map[string]any `json:"messages"`
				}
				path := "/api/v1/messages?topic_id=" + url.QueryEscape(topicID) + "&limit=20&status=pending"
				if err := client.do(http.MethodGet, path, nil, &out); err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
				}
				for _, m := range out.Messages {
					id, _ := m["id"].(string)
					if seen[id] {
						continue
					}
					seen[id] = true
					printInboxMessage(m)
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
	return cmd
}

// newBroadcastCommand implements `opencortex broadcast <message>`.
func newBroadcastCommand(cfgPath, baseURL, apiKey *string) *cobra.Command {
	return &cobra.Command{
		Use:   "broadcast <message>",
		Short: "Broadcast a message to all agents",
		Args:  cobra.ExactArgs(1),
		Example: strings.TrimSpace(`
  opencortex broadcast "All agents: deploying v2.1 in 5 minutes"`),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/messages/broadcast", map[string]any{
				"content":      args[0],
				"content_type": "text/plain",
				"priority":     "normal",
			}, &out); err != nil {
				return err
			}
			if msg, ok := out["message"].(map[string]any); ok {
				fmt.Printf("broadcast sent: %s\n", msg["id"])
			} else {
				fmt.Println("broadcast sent")
			}
			return nil
		},
	}
}

// newMCPCommand is the `opencortex mcp` alias for `mcp-server`.
// On localhost it auto-registers so no --api-key is required.
func newMCPCommand(cfgPath *string) *cobra.Command {
	var logLevel string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP server over stdio (zero-config on localhost)",
		Long: strings.TrimSpace(`
Starts the MCP server over stdio. On localhost, auto-registers this process
as an agent so no --api-key is needed.

MCP config (minimal):
  {"mcpServers": {"opencortex": {"command": "opencortex", "args": ["mcp"]}}}`),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigMaybe(*cfgPath)
			if err != nil {
				return err
			}
			if strings.TrimSpace(logLevel) != "" {
				cfg.Logging.Level = strings.TrimSpace(logLevel)
			}
			if !cfg.MCP.Enabled {
				return errors.New("mcp is disabled in config")
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
			if err := store.SeedRBAC(ctx); err != nil {
				return err
			}
			memBroker := broker.NewMemory(cfg.Broker.ChannelBufferSize)
			app := service.New(cfg, store, memBroker)
			if _, err := app.EnsureBroadcastSetup(ctx, ""); err != nil && !errors.Is(err, service.ErrNotFound) {
				return err
			}
			syncEngine := syncer.NewEngine(db, store)
			handler := handlers.New(app, db, cfg, syncEngine)
			hub := ws.NewHub(app, store)
			apiRouter := api.NewRouter(handler, app, hub)

			// Auto-register via the service layer directly (we are in-process).
			serverURL := discoverServerURL()
			hostname, _ := os.Hostname()
			profile, _, _ := resolveProfile(serverURL, agentProfileFlag)
			fp := localFingerprint(serverURL, profile)
			agentName := "mcp@" + hostname + ":" + profile
			_, effectiveKey, autoErr := app.AutoRegisterLocal(ctx, agentName, fp)
			if autoErr != nil {
				// Fall back to configured key.
				effectiveKey = strings.TrimSpace(cfg.Auth.AdminKey)
			}
			if effectiveKey == "" {
				return errors.New("could not obtain API key; set auth.admin_key in config or start the server first")
			}
			// Persist for future CLI calls.
			_ = authStoreUpsertWithProfile(serverURL, profile, effectiveKey, map[string]any{
				"name": agentName,
			}, []string{"agent"})

			mcpBridge := mcpbridge.New(mcpbridge.Options{
				App:           app,
				Config:        cfg,
				Router:        apiRouter,
				DefaultAPIKey: effectiveKey,
			})
			log.Printf("opencortex mcp started (stdio)")
			return mcpBridge.ServeStdio()
		},
	}
	cmd.Flags().StringVar(&logLevel, "log-level", "", "Log level override")
	return cmd
}

// ─── Existing command constructors follow ─────────────────────────────────────

func newInitCommand(cfgPath *string) *cobra.Command {
	var (
		adminName  string
		all        bool
		mcpOnly    bool
		vscodeOnly bool
		show       bool
		silent     bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap local opencortex runtime and integrations",
		Example: strings.TrimSpace(`
  opencortex init
  opencortex init --all --silent
  opencortex init --mcp-only
  opencortex init --vscode-only
  opencortex init --show`),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(os.Stderr, "warning: `init` is legacy and will be removed; use `start` and `doctor`.")
			if show {
				return printJSON(map[string]any{"status": bootstrap.CurrentStatus()})
			}
			if mcpOnly && vscodeOnly {
				return errors.New("--mcp-only and --vscode-only are mutually exclusive")
			}
			if !all && !mcpOnly && !vscodeOnly {
				all = true
			}
			if *cfgPath == bootstrap.ManagedConfigPath() {
				if _, err := bootstrap.EnsureManagedConfig(*cfgPath); err != nil {
					return err
				}
			}
			cfg, err := loadConfigMaybe(*cfgPath)
			if err != nil {
				return err
			}
			if strings.TrimSpace(adminName) != "" {
				cfg.Auth.AdminKey = ""
			}
			state, err := runBootstrap(context.Background(), *cfgPath, cfg, bootstrapRunOptions{
				All:        all,
				MCPOnly:    mcpOnly,
				VSCodeOnly: vscodeOnly,
				Silent:     silent,
				ServerURL:  fmt.Sprintf("http://localhost:%d", cfg.Server.Port),
				AdminName:  adminName,
			})
			if err != nil {
				return err
			}
			if silent {
				return nil
			}
			fmt.Println("OpenCortex bootstrap complete.")
			fmt.Printf("  Config:    %s\n", filepath.Clean(*cfgPath))
			fmt.Printf("  Database:  %s\n", filepath.Clean(cfg.Database.Path))
			fmt.Printf("  Server:    %s\n", strings.TrimSpace(state.ServerURL))
			fmt.Printf("  Copilot:   %v\n", state.CopilotMCPConfigured)
			fmt.Printf("  Codex:     %v\n", state.CodexMCPConfigured)
			fmt.Printf("  VSCode:    %v\n", state.VSCodeExtensionInstalled)
			fmt.Printf("  Autostart: %v\n", state.AutostartConfigured)
			return nil
		},
	}
	cmd.Flags().StringVar(&adminName, "admin-name", "", "Name for bootstrap admin agent (forces admin key rotation on init)")
	cmd.Flags().BoolVar(&all, "all", false, "Run full bootstrap (default)")
	cmd.Flags().BoolVar(&mcpOnly, "mcp-only", false, "Only update MCP configs")
	cmd.Flags().BoolVar(&vscodeOnly, "vscode-only", false, "Only install VSCode extension if available")
	cmd.Flags().BoolVar(&show, "show", false, "Show current bootstrap state")
	cmd.Flags().BoolVar(&silent, "silent", false, "Suppress non-essential output")
	cmd.Hidden = true
	return cmd
}

func newServerCommand(cfgPath *string) *cobra.Command {
	var (
		mcpHTTPEnabled bool
		mcpHTTPPath    string
		noAutostart    bool
		openBrowser    bool
	)
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run opencortex server",
		Example: strings.TrimSpace(`
  opencortex server
  opencortex server --config ./config.yaml`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if *cfgPath == bootstrap.ManagedConfigPath() {
				if _, err := bootstrap.EnsureManagedConfig(*cfgPath); err != nil {
					return err
				}
			}
			cfg, err := loadConfigMaybe(*cfgPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("mcp-http-enabled") {
				cfg.MCP.HTTP.Enabled = mcpHTTPEnabled
			}
			if cmd.Flags().Changed("mcp-http-path") && strings.TrimSpace(mcpHTTPPath) != "" {
				cfg.MCP.HTTP.Path = strings.TrimSpace(mcpHTTPPath)
			}

			port, portErr := selectAvailablePort(cfg.Server.Port, 10)
			if portErr != nil {
				return portErr
			}
			if port != cfg.Server.Port {
				fmt.Printf("Port %d is in use. Trying %d... ✓\n", cfg.Server.Port, port)
				cfg.Server.Port = port
				if err := saveConfig(*cfgPath, cfg); err != nil {
					return err
				}
			}

			serverURL := fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
			_, stateErr := os.Stat(bootstrap.StatePath())
			firstRun := errors.Is(stateErr, os.ErrNotExist)
			if firstRun {
				if _, err := runBootstrap(context.Background(), *cfgPath, cfg, bootstrapRunOptions{
					All:         true,
					Silent:      true,
					NoAutostart: noAutostart,
					ServerURL:   serverURL,
				}); err != nil {
					return err
				}
			}

			// ── Single-instance enforcement ──────────────────────────────────
			existingPID, serverAddr, lockErr := acquireServerLock(cfg.Server.Port)
			if errors.Is(lockErr, errAlreadyRunning) {
				fmt.Printf("OpenCortex is already running on this host (pid %d).\nDashboard → %s\n",
					existingPID, serverAddr)
				return nil
			}
			if lockErr != nil {
				return fmt.Errorf("acquire server lock: %w", lockErr)
			}
			defer releaseServerLock()
			defer removeServerFile()
			// ─────────────────────────────────────────────────────────────────

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

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
			if _, err := app.EnsureBroadcastSetup(ctx, ""); err != nil && !errors.Is(err, service.ErrNotFound) {
				return err
			}
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
			var mcpHTTPHandler http.Handler
			if cfg.MCP.Enabled && cfg.MCP.HTTP.Enabled {
				mcpBridge := mcpbridge.New(mcpbridge.Options{
					App:    app,
					Config: cfg,
					Router: apiRouter,
				})
				mcpHTTPHandler = mcpBridge.HTTPHandler()
			}
			uiAssets := webui.FS()
			uiFS := http.FileServer(http.FS(uiAssets))
			serverMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case mcpHTTPHandler != nil && strings.HasPrefix(r.URL.Path, cfg.MCP.HTTP.Path):
					mcpHTTPHandler.ServeHTTP(w, r)
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
			serverURL = fmt.Sprintf("http://%s", httpServer.Addr)
			if httpServer.Addr == "" || strings.HasPrefix(httpServer.Addr, ":") {
				serverURL = fmt.Sprintf("http://localhost%s", httpServer.Addr)
			}
			writeServerFile(serverURL)
			log.Printf("opencortex server listening on %s", config.Addr(cfg))
			log.Printf("connect your agents to %s", serverURL)
			if (firstRun && openBrowser) || (openBrowser && cmd.Flags().Changed("open-browser")) {
				go func(target string) {
					time.Sleep(700 * time.Millisecond)
					maybeOpenBrowser(target)
				}(serverURL)
			}
			return httpServer.ListenAndServe()

		},
	}
	cmd.Flags().BoolVar(&mcpHTTPEnabled, "mcp-http-enabled", true, "Enable MCP streamable HTTP endpoint")
	cmd.Flags().StringVar(&mcpHTTPPath, "mcp-http-path", "", "Override MCP streamable HTTP endpoint path")
	cmd.Flags().BoolVar(&noAutostart, "no-autostart", false, "Disable auto-start setup during bootstrap")
	cmd.Flags().BoolVar(&openBrowser, "open-browser", true, "Open dashboard in browser on first run")
	return cmd
}

func newMCPServerCommand(cfgPath *string) *cobra.Command {
	var (
		apiKey   string
		agentID  string
		logLevel string
	)
	cmd := &cobra.Command{
		Use:   "mcp-server",
		Short: "Run Opencortex MCP server over stdio",
		Example: strings.TrimSpace(`
  opencortex mcp-server --config ./config.yaml --api-key amk_live_xxx
  opencortex mcp-server --config ./config.yaml --api-key amk_live_xxx --agent-id <agent-id>`),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigMaybe(*cfgPath)
			if err != nil {
				return err
			}
			if strings.TrimSpace(logLevel) != "" {
				cfg.Logging.Level = strings.TrimSpace(logLevel)
			}
			if !cfg.MCP.Enabled {
				return errors.New("mcp is disabled in config")
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
			if err := store.SeedRBAC(ctx); err != nil {
				return err
			}
			memBroker := broker.NewMemory(cfg.Broker.ChannelBufferSize)
			app := service.New(cfg, store, memBroker)
			if _, err := app.EnsureBroadcastSetup(ctx, ""); err != nil && !errors.Is(err, service.ErrNotFound) {
				return err
			}
			syncEngine := syncer.NewEngine(db, store)
			handler := handlers.New(app, db, cfg, syncEngine)
			hub := ws.NewHub(app, store)
			apiRouter := api.NewRouter(handler, app, hub)

			effectiveKey := strings.TrimSpace(apiKey)
			if effectiveKey == "" {
				effectiveKey = strings.TrimSpace(cfg.Auth.AdminKey)
			}
			if effectiveKey == "" {
				return errors.New("api key is required for stdio mode (use --api-key or set auth.admin_key)")
			}

			mcpBridge := mcpbridge.New(mcpbridge.Options{
				App:            app,
				Config:         cfg,
				Router:         apiRouter,
				DefaultAPIKey:  effectiveKey,
				DefaultAgentID: strings.TrimSpace(agentID),
			})
			log.Printf("opencortex mcp-server started (stdio)")
			return mcpBridge.ServeStdio()
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Default API key used by stdio MCP calls")
	cmd.Flags().StringVar(&agentID, "agent-id", "", "Optional expected agent ID for the API key")
	cmd.Flags().StringVar(&logLevel, "log-level", "", "Optional log level override")
	return cmd
}

func newConfigCommand(cfgPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config file tools and schema help",
		Long: strings.TrimSpace(`
Configuration scheme overview:
  mode: local | server | hybrid
  server: host/port/timeouts/cors
  mcp: stdio/http transport, lease defaults, tool exposure
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
  OPENCORTEX_MCP_ENABLED
  OPENCORTEX_MCP_HTTP_ENABLED
  OPENCORTEX_MCP_HTTP_PATH
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
			cfg.MCP.Enabled = promptBool(reader, "mcp.enabled", cfg.MCP.Enabled)
			cfg.MCP.HTTP.Enabled = promptBool(reader, "mcp.http.enabled", cfg.MCP.HTTP.Enabled)
			cfg.MCP.HTTP.Path = prompt(reader, "mcp.http.path", cfg.MCP.HTTP.Path)
			cfg.MCP.Delivery.DefaultLeaseSeconds = promptInt(reader, "mcp.delivery.default_lease_seconds", cfg.MCP.Delivery.DefaultLeaseSeconds)
			cfg.MCP.Delivery.MaxLeaseSeconds = promptInt(reader, "mcp.delivery.max_lease_seconds", cfg.MCP.Delivery.MaxLeaseSeconds)
			cfg.MCP.Tools.FullParity = promptBool(reader, "mcp.tools.full_parity", cfg.MCP.Tools.FullParity)
			cfg.MCP.Tools.ExposeAdmin = promptBool(reader, "mcp.tools.expose_admin", cfg.MCP.Tools.ExposeAdmin)

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

func newAuthCommand(baseURL, apiKey, agentProfile *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate CLI and persist credentials",
		Long: strings.TrimSpace(`
Persistent auth works similarly to gh:
  - login stores credentials for a host/base URL + profile
  - status shows saved accounts and current selection
  - whoami validates current credentials against /api/v1/agents/me
  - switch changes the active saved account profile
  - logout removes one or all saved accounts

When --api-key is omitted, CLI commands use the saved account for --base-url and --agent-profile.`),
		Example: strings.TrimSpace(`
  opencortex auth login --base-url http://localhost:8080 --api-key amk_live_xxx
  opencortex auth profiles use planner
  opencortex auth status
  opencortex auth whoami
  opencortex auth switch --base-url https://hub.example.com --agent-profile planner
  opencortex auth logout --base-url http://localhost:8080`),
	}

	resolveCmdProfile := func(base string) (string, error) {
		profile, _, err := resolveProfile(base, *agentProfile)
		return profile, err
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
			profile, err := resolveCmdProfile(client.baseURL)
			if err != nil {
				return err
			}
			if err := authStoreUpsertWithProfile(client.baseURL, profile, key, out.Agent, out.Roles); err != nil {
				return err
			}
			fmt.Printf("logged in to %s (profile: %s)\n", client.baseURL, profile)
			if *asJSON {
				return printJSON(map[string]any{
					"base_url": client.baseURL,
					"profile":  profile,
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
					BaseURL   string    `json:"base_url"`
					Profile   string    `json:"profile"`
					AgentID   string    `json:"agent_id,omitempty"`
					AgentName string    `json:"agent_name,omitempty"`
					Roles     []string  `json:"roles,omitempty"`
					UpdatedAt time.Time `json:"updated_at"`
					HasToken  bool      `json:"has_token"`
					IsCurrent bool      `json:"is_current"`
				}
				out := make([]entry, 0, len(store.Accounts))
				base := canonicalBaseURL(*baseURL)
				activeProfile, _ := authStoreCurrentProfile(base)
				for _, v := range store.Accounts {
					if base != "" && v.BaseURL != base {
						continue
					}
					out = append(out, entry{
						BaseURL:   v.BaseURL,
						Profile:   v.Profile,
						AgentID:   v.AgentID,
						AgentName: v.AgentName,
						Roles:     v.Roles,
						UpdatedAt: v.UpdatedAt,
						HasToken:  strings.TrimSpace(v.APIKey) != "",
						IsCurrent: v.BaseURL == base && v.Profile == activeProfile,
					})
				}
				sort.Slice(out, func(i, j int) bool {
					if out[i].BaseURL == out[j].BaseURL {
						return out[i].Profile < out[j].Profile
					}
					return out[i].BaseURL < out[j].BaseURL
				})
				return printJSON(map[string]any{
					"current_base_url": base,
					"current_profile":  activeProfile,
					"accounts":         out,
				})
			}
			if len(store.Accounts) == 0 {
				fmt.Println("no saved auth accounts")
				return nil
			}
			type item struct {
				Key string
				Acc authAccount
			}
			items := make([]item, 0, len(store.Accounts))
			for k, a := range store.Accounts {
				items = append(items, item{Key: k, Acc: a})
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
			base := canonicalBaseURL(*baseURL)
			currentProfile, _ := authStoreCurrentProfile(base)
			for _, it := range items {
				a := it.Acc
				if base != "" && a.BaseURL != base {
					continue
				}
				active := " "
				if a.BaseURL == base && a.Profile == currentProfile {
					active = "*"
				}
				fmt.Printf("%s %s#%s", active, a.BaseURL, a.Profile)
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
		Short: "Switch active saved account to --base-url and profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			profile, err := resolveCmdProfile(base)
			if err != nil {
				return err
			}
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			key := authAccountKey(base, profile)
			if _, ok := store.Accounts[key]; !ok {
				return fmt.Errorf("no saved account for %s profile %s", base, profile)
			}
			if err := authStoreSetCurrentProfile(base, profile); err != nil {
				return err
			}
			fmt.Printf("active account: %s (profile: %s)\n", base, profile)
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
				store.CurrentByBaseURL = map[string]string{}
				if err := authStoreSave(store); err != nil {
					return err
				}
				fmt.Println("removed all saved accounts")
				return nil
			}
			base := canonicalBaseURL(*baseURL)
			profile, err := resolveCmdProfile(base)
			if err != nil {
				return err
			}
			delete(store.Accounts, authAccountKey(base, profile))
			if cur, ok := store.CurrentByBaseURL[base]; ok && cur == profile {
				delete(store.CurrentByBaseURL, base)
				for _, acc := range store.Accounts {
					if acc.BaseURL == base {
						store.CurrentByBaseURL[base] = acc.Profile
						break
					}
				}
			}
			if err := authStoreSave(store); err != nil {
				return err
			}
			fmt.Printf("logged out from %s (profile: %s)\n", base, profile)
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
			profile, err := resolveCmdProfile(base)
			if err != nil {
				return err
			}
			token, ok := authStoreGetTokenForProfile(base, profile)
			if !ok && base == canonicalBaseURL("http://localhost:8080") {
				token, ok = authStoreGetToken(base)
			}
			if !ok {
				return errors.New("no saved token; run 'opencortex auth login'")
			}
			fmt.Println(token)
			return nil
		},
	})

	var forceDeleteProfile bool
	profiles := &cobra.Command{
		Use:   "profiles",
		Short: "Manage persisted agent profiles for auth identity",
	}
	profiles.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List profiles for --base-url",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			seen := map[string]struct{}{}
			out := []string{}
			for _, acc := range store.Accounts {
				if acc.BaseURL != base {
					continue
				}
				if _, ok := seen[acc.Profile]; ok {
					continue
				}
				seen[acc.Profile] = struct{}{}
				out = append(out, acc.Profile)
			}
			if _, ok := seen[defaultAgentProfile]; !ok {
				out = append(out, defaultAgentProfile)
			}
			sort.Strings(out)
			current, _ := authStoreCurrentProfile(base)
			if *asJSON {
				return printJSON(map[string]any{"base_url": base, "current_profile": current, "profiles": out})
			}
			for _, p := range out {
				marker := " "
				if p == current {
					marker = "*"
				}
				fmt.Printf("%s %s\n", marker, p)
			}
			return nil
		},
	})
	profiles.AddCommand(&cobra.Command{
		Use:   "use <name>",
		Short: "Set current profile for --base-url",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			p, err := normalizeProfileName(args[0])
			if err != nil {
				return err
			}
			if err := authStoreSetCurrentProfile(base, p); err != nil {
				return err
			}
			fmt.Printf("active profile for %s: %s\n", base, p)
			return nil
		},
	})
	profiles.AddCommand(&cobra.Command{
		Use:   "show [name]",
		Short: "Show account metadata for a profile on --base-url",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			p, _ := authStoreCurrentProfile(base)
			if len(args) == 1 {
				var err error
				p, err = normalizeProfileName(args[0])
				if err != nil {
					return err
				}
			}
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			acc, ok := store.Accounts[authAccountKey(base, p)]
			if !ok {
				if *asJSON {
					return printJSON(map[string]any{"base_url": base, "profile": p, "exists": false})
				}
				fmt.Printf("profile %s has no saved account for %s\n", p, base)
				return nil
			}
			return printJSON(map[string]any{"base_url": base, "profile": p, "account": acc})
		},
	})
	profiles.AddCommand(&cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename profile for --base-url",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			oldP, err := normalizeProfileName(args[0])
			if err != nil {
				return err
			}
			newP, err := normalizeProfileName(args[1])
			if err != nil {
				return err
			}
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			oldKey := authAccountKey(base, oldP)
			acc, ok := store.Accounts[oldKey]
			if !ok {
				return fmt.Errorf("profile %s not found for %s", oldP, base)
			}
			newKey := authAccountKey(base, newP)
			if _, exists := store.Accounts[newKey]; exists {
				return fmt.Errorf("profile %s already exists for %s", newP, base)
			}
			delete(store.Accounts, oldKey)
			acc.Profile = newP
			store.Accounts[newKey] = acc
			if cur, ok := store.CurrentByBaseURL[base]; ok && cur == oldP {
				store.CurrentByBaseURL[base] = newP
			}
			if err := authStoreSave(store); err != nil {
				return err
			}
			fmt.Printf("renamed profile %s -> %s for %s\n", oldP, newP, base)
			return nil
		},
	})
	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete profile account for --base-url",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := canonicalBaseURL(*baseURL)
			p, err := normalizeProfileName(args[0])
			if err != nil {
				return err
			}
			store, err := authStoreLoad()
			if err != nil {
				return err
			}
			key := authAccountKey(base, p)
			if _, ok := store.Accounts[key]; !ok {
				return fmt.Errorf("profile %s not found for %s", p, base)
			}
			cur, _ := authStoreCurrentProfile(base)
			if cur == p && !forceDeleteProfile {
				return fmt.Errorf("profile %s is active; use --force or switch profile first", p)
			}
			delete(store.Accounts, key)
			if cur == p {
				delete(store.CurrentByBaseURL, base)
				for _, acc := range store.Accounts {
					if acc.BaseURL == base {
						store.CurrentByBaseURL[base] = acc.Profile
						break
					}
				}
			}
			if err := authStoreSave(store); err != nil {
				return err
			}
			fmt.Printf("deleted profile %s for %s\n", p, base)
			return nil
		},
	}
	deleteCmd.Flags().BoolVar(&forceDeleteProfile, "force", false, "Allow deleting current active profile")
	profiles.AddCommand(deleteCmd)
	cmd.AddCommand(profiles)

	return cmd
}

func newAgentsCommand(cfgPath, baseURL, apiKey *string, asJSON *bool) *cobra.Command {
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
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(http.MethodPost, "/api/v1/agents", map[string]any{
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*baseURL, *apiKey)
			var out map[string]any
			path := "/api/v1/knowledge?q=" + url.QueryEscape(args[0])
			if err := client.do(http.MethodGet, path, nil, &out); err != nil {
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
		Args:  cobra.ExactArgs(1),
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

func newSyncCommand(cfgPath, baseURL, apiKey *string, asJSON *bool) *cobra.Command {
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
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(http.MethodPost, "/api/v1/sync/remotes", map[string]any{
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
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/sync/remotes", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})
	cmd.AddCommand(remoteCmd)

	cmd.AddCommand(syncPushPullCommand("push", cfgPath, baseURL, apiKey))
	cmd.AddCommand(syncPushPullCommand("pull", cfgPath, baseURL, apiKey))

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show sync status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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

func syncPushPullCommand(kind string, cfgPath, baseURL, apiKey *string) *cobra.Command {
	var key string
	short := "Push data to remote"
	if kind == "pull" {
		short = "Pull data from remote"
	}
	cmd := &cobra.Command{
		Use:   kind + " <remote>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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

func newAdminCommand(cfgPath, baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Admin commands",
		Example: strings.TrimSpace(`
  opencortex admin stats --api-key <admin-key>
  opencortex admin backup --api-key <admin-key>
  opencortex admin rbac roles --api-key <admin-key>
  opencortex admin rbac assign --agent <agent-id> --role agent --api-key <admin-key>`),
	}
	cmd.AddCommand(adminSimpleCommand("stats", "/api/v1/admin/stats", cfgPath, baseURL, apiKey))
	cmd.AddCommand(adminSimpleCommand("backup", "/api/v1/admin/backup", cfgPath, baseURL, apiKey))
	cmd.AddCommand(adminSimpleCommand("vacuum", "/api/v1/admin/vacuum", cfgPath, baseURL, apiKey))

	rbac := &cobra.Command{Use: "rbac", Short: "RBAC commands"}
	rbac.AddCommand(adminSimpleCommand("roles", "/api/v1/admin/rbac/roles", cfgPath, baseURL, apiKey))

	var agentID, role string
	assign := &cobra.Command{
		Use:   "assign",
		Short: "Assign role to an agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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

func adminSimpleCommand(use, path string, cfgPath, baseURL, apiKey *string) *cobra.Command {
	method := http.MethodGet
	if strings.Contains(path, "backup") || strings.Contains(path, "vacuum") {
		method = http.MethodPost
	}
	short := simpleTitle(use)
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
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
		path = bootstrap.ManagedConfigPath()
	}
	if _, err := os.Stat(path); err == nil {
		return config.Load(path)
	} else if errors.Is(err, os.ErrNotExist) {
		if path == bootstrap.ManagedConfigPath() {
			return bootstrap.EnsureManagedConfig(path)
		}
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
	Profile   string    `json:"profile,omitempty"`
	APIKey    string    `json:"api_key"`
	AgentID   string    `json:"agent_id,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	Roles     []string  `json:"roles,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type authStore struct {
	Current          string                 `json:"current"`
	CurrentByBaseURL map[string]string      `json:"current_by_base_url,omitempty"`
	Accounts         map[string]authAccount `json:"accounts"`
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
			return authStore{
				CurrentByBaseURL: map[string]string{},
				Accounts:         map[string]authAccount{},
			}, nil
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
	if s.CurrentByBaseURL == nil {
		s.CurrentByBaseURL = map[string]string{}
	}
	// Lazy migration: legacy accounts keyed by base_url migrate to base_url#default.
	migrated := map[string]authAccount{}
	for k, acc := range s.Accounts {
		if strings.Contains(k, "#") {
			if acc.Profile == "" {
				parts := strings.SplitN(k, "#", 2)
				acc.Profile = parts[1]
			}
			if acc.BaseURL == "" {
				parts := strings.SplitN(k, "#", 2)
				acc.BaseURL = canonicalBaseURL(parts[0])
			}
			migrated[k] = acc
			continue
		}
		base := canonicalBaseURL(k)
		if base == "" {
			base = canonicalBaseURL(acc.BaseURL)
		}
		if base == "" {
			continue
		}
		if acc.BaseURL == "" {
			acc.BaseURL = base
		}
		if acc.Profile == "" {
			acc.Profile = defaultAgentProfile
		}
		migrated[authAccountKey(base, acc.Profile)] = acc
		if _, ok := s.CurrentByBaseURL[base]; !ok {
			s.CurrentByBaseURL[base] = acc.Profile
		}
	}
	s.Accounts = migrated
	// Legacy current used to be base URL.
	if s.Current != "" {
		base := canonicalBaseURL(s.Current)
		if base != "" {
			if _, ok := s.CurrentByBaseURL[base]; !ok {
				s.CurrentByBaseURL[base] = defaultAgentProfile
			}
		}
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
	if store.CurrentByBaseURL == nil {
		store.CurrentByBaseURL = map[string]string{}
	}
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func authAccountKey(baseURL, profile string) string {
	baseURL = canonicalBaseURL(baseURL)
	if profile == "" {
		profile = defaultAgentProfile
	}
	return baseURL + "#" + profile
}

func authStoreCurrentProfile(baseURL string) (string, bool) {
	store, err := authStoreLoad()
	if err != nil {
		return "", false
	}
	baseURL = canonicalBaseURL(baseURL)
	if baseURL == "" {
		baseURL = canonicalBaseURL("http://localhost:8080")
	}
	if p, ok := store.CurrentByBaseURL[baseURL]; ok && strings.TrimSpace(p) != "" {
		return p, true
	}
	return defaultAgentProfile, false
}

func authStoreSetCurrentProfile(baseURL, profile string) error {
	baseURL = canonicalBaseURL(baseURL)
	profile, err := normalizeProfileName(profile)
	if err != nil {
		return err
	}
	store, err := authStoreLoad()
	if err != nil {
		return err
	}
	if store.CurrentByBaseURL == nil {
		store.CurrentByBaseURL = map[string]string{}
	}
	store.CurrentByBaseURL[baseURL] = profile
	store.Current = baseURL
	return authStoreSave(store)
}

func authStoreUpsertWithProfile(baseURL, profile, key string, agent any, roles []string) error {
	baseURL = canonicalBaseURL(baseURL)
	profile, err := normalizeProfileName(profile)
	if err != nil {
		return err
	}
	store, err := authStoreLoad()
	if err != nil {
		return err
	}

	acc := authAccount{
		BaseURL:   baseURL,
		Profile:   profile,
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

	store.Accounts[authAccountKey(baseURL, profile)] = acc
	if store.CurrentByBaseURL == nil {
		store.CurrentByBaseURL = map[string]string{}
	}
	store.CurrentByBaseURL[baseURL] = profile
	store.Current = baseURL
	return authStoreSave(store)
}

func authStoreUpsert(baseURL, key string, agent any, roles []string) error {
	profile, _, _ := resolveProfile(baseURL, agentProfileFlag)
	return authStoreUpsertWithProfile(baseURL, profile, key, agent, roles)
}

func authStoreGetTokenForProfile(baseURL, profile string) (string, bool) {
	store, err := authStoreLoad()
	if err != nil {
		return "", false
	}
	baseURL = canonicalBaseURL(baseURL)
	profile, err = normalizeProfileName(profile)
	if err != nil {
		return "", false
	}
	if baseURL != "" {
		if acc, ok := store.Accounts[authAccountKey(baseURL, profile)]; ok && strings.TrimSpace(acc.APIKey) != "" {
			return acc.APIKey, true
		}
		if p, ok := store.CurrentByBaseURL[baseURL]; ok {
			if acc, ok := store.Accounts[authAccountKey(baseURL, p)]; ok && strings.TrimSpace(acc.APIKey) != "" {
				return acc.APIKey, true
			}
		}
		if acc, ok := store.Accounts[authAccountKey(baseURL, defaultAgentProfile)]; ok && strings.TrimSpace(acc.APIKey) != "" {
			return acc.APIKey, true
		}
	}
	return "", false
}

func authStoreGetToken(baseURL string) (string, bool) {
	profile, _, _ := resolveProfile(baseURL, agentProfileFlag)
	return authStoreGetTokenForProfile(baseURL, profile)
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
		fmt.Fprintf(b, "  %sLifecycle%s: start/stop/status/doctor for local runtime\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sAgents%s: register, inspect, rotate keys\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sKnowledge%s: add, search, version, export/import entries\n", ansiGreen, ansiReset)
		fmt.Fprintf(b, "  %sSkills%s: manage shared skillsets and install local projections\n", ansiGreen, ansiReset)
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
		fmt.Fprintf(b, "  %sStart and verify local runtime%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex start\n")
		fmt.Fprintf(b, "    opencortex status\n\n")

		fmt.Fprintf(b, "  %sDiagnose local issues%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex doctor\n")
		fmt.Fprintf(b, "    opencortex doctor --fix\n\n")

		fmt.Fprintf(b, "  %sRun MCP stdio server%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex mcp-server --config ./config.yaml --api-key <key>\n\n")

		fmt.Fprintf(b, "  %sPush first knowledge item%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex knowledge add --title \"System Scope\" --file ./scope.md --tags scope,mvp --api-key <key>\n")
		fmt.Fprintf(b, "    opencortex knowledge search \"scope\" --api-key <key>\n\n")

		fmt.Fprintf(b, "  %sDiscover skills help%s\n", ansiGray, ansiReset)
		fmt.Fprintf(b, "    opencortex skills --help\n")
		fmt.Fprintf(b, "    opencortex skills install --help\n\n")

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
	if cmd.CommandPath() == "opencortex" {
		priority := map[string]int{
			"start":     1,
			"stop":      2,
			"status":    3,
			"doctor":    4,
			"send":      5,
			"inbox":     6,
			"broadcast": 7,
			"watch":     8,
			"skills":    9,
			"agents":    10,
		}
		sort.Slice(out, func(i, j int) bool {
			pi, iok := priority[out[i].Name()]
			pj, jok := priority[out[j].Name()]
			switch {
			case iok && jok:
				return pi < pj
			case iok:
				return true
			case jok:
				return false
			default:
				return out[i].Name() < out[j].Name()
			}
		})
		return out
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
