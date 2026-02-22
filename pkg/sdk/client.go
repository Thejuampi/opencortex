package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ConnectOptions configures the zero-config Connect() entry point.
// All fields are optional — safe to call as sdk.Connect(ctx, sdk.ConnectOptions{}).
type ConnectOptions struct {
	// URL is the OpenCortex server URL.
	// Empty → OPENCORTEX_URL env var → http://localhost:8080.
	URL string

	// APIKey is the API key to use. Empty → auto-register on localhost.
	APIKey string

	// AgentName is the label used for auto-registration.
	// Empty → "sdk-agent".
	AgentName string

	// Fingerprint is a stable identity string across restarts.
	// Empty → auto-register always creates a new agent.
	Fingerprint string

	// Timeout is the HTTP client timeout. Default: 30s.
	Timeout time.Duration
}

// Connect creates a fully wired Client with zero ceremony.
//
//   - Discovers the server URL automatically.
//   - On localhost, auto-registers this process as an agent (no API key needed).
//   - Returns a ready Client with all service accessors populated.
//
// Example:
//
//	client, err := sdk.Connect(ctx, sdk.ConnectOptions{AgentName: "my-agent"})
func Connect(ctx context.Context, opts ConnectOptions) (*Client, error) {
	baseURL := resolveURL(opts.URL)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	apiKey := opts.APIKey
	if apiKey == "" {
		if isLocalhost(baseURL) {
			var err error
			apiKey, err = autoRegister(ctx, baseURL, opts.AgentName, opts.Fingerprint, timeout)
			if err != nil {
				return nil, fmt.Errorf("sdk.Connect: auto-register failed: %w", err)
			}
		}
	}
	return New(Config{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Timeout: timeout,
	}), nil
}

func resolveURL(override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("OPENCORTEX_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:8080"
}

func isLocalhost(rawURL string) bool {
	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		if strings.Contains(rawURL, h) {
			return true
		}
	}
	return false
}

func autoRegister(ctx context.Context, baseURL, agentName, fingerprint string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(agentName) == "" {
		agentName = "sdk-agent"
	}
	body, _ := json.Marshal(map[string]string{
		"name":        agentName,
		"fingerprint": fingerprint,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/api/v1/agents/auto-register",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("auto-register: server returned %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			APIKey string `json:"api_key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK || out.Data.APIKey == "" {
		return "", fmt.Errorf("auto-register: empty key in response")
	}
	return out.Data.APIKey, nil
}

type Config struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	Agents      *AgentsService
	Messages    *MessagesService
	Topics      *TopicsService
	Knowledge   *KnowledgeService
	Collections *CollectionsService
	Sync        *SyncService
	Admin       *AdminService
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8080"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	c := &Client{
		BaseURL: strings.TrimRight(cfg.BaseURL, "/"),
		APIKey:  cfg.APIKey,
		HTTP:    &http.Client{Timeout: cfg.Timeout},
	}
	c.Agents = &AgentsService{client: c}
	c.Messages = &MessagesService{client: c}
	c.Topics = &TopicsService{client: c}
	c.Knowledge = &KnowledgeService{client: c}
	c.Collections = &CollectionsService{client: c}
	c.Sync = &SyncService{client: c}
	c.Admin = &AdminService{client: c}
	return c
}

type envelope[T any] struct {
	OK    bool `json:"ok"`
	Data  T    `json:"data"`
	Error any  `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var raw envelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return err
	}
	if !raw.OK {
		return fmt.Errorf("api error: %v", raw.Error)
	}
	if out != nil {
		return json.Unmarshal(raw.Data, out)
	}
	return nil
}

type AgentsService struct{ client *Client }
type TopicsService struct{ client *Client }
type CollectionsService struct{ client *Client }
type SyncService struct{ client *Client }
type AdminService struct{ client *Client }
