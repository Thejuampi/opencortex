package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
