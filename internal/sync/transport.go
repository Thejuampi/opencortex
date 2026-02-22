package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Transport struct {
	Client *http.Client
}

func NewTransport() *Transport {
	return &Transport{
		Client: &http.Client{Timeout: 30 * time.Second},
	}
}

type DiffRequest struct {
	Scope  string         `json:"scope"`
	Items  []ManifestItem `json:"items"`
	Remote string         `json:"remote,omitempty"`
}

type DiffResponse struct {
	Need []ManifestItem `json:"need"`
	Have []ManifestItem `json:"have"`
}

func (t *Transport) Diff(ctx context.Context, remoteURL, apiKey string, req DiffRequest) (DiffResponse, error) {
	endpoint := strings.TrimSuffix(remoteURL, "/") + "/api/v1/sync/diff"
	var res DiffResponse
	if err := t.do(ctx, endpoint, apiKey, req, &res); err != nil {
		return DiffResponse{}, err
	}
	return res, nil
}

func (t *Transport) Push(ctx context.Context, remoteURL, apiKey string, payload map[string]any) (map[string]any, error) {
	endpoint := strings.TrimSuffix(remoteURL, "/") + "/api/v1/sync/push"
	out := map[string]any{}
	if err := t.do(ctx, endpoint, apiKey, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *Transport) Pull(ctx context.Context, remoteURL, apiKey string, payload map[string]any) (map[string]any, error) {
	endpoint := strings.TrimSuffix(remoteURL, "/") + "/api/v1/sync/pull"
	out := map[string]any{}
	if err := t.do(ctx, endpoint, apiKey, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *Transport) do(ctx context.Context, endpoint, apiKey string, reqBody any, out any) error {
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("remote status %d", resp.StatusCode)
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Data  any  `json:"data"`
		Error any  `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return fmt.Errorf("remote error: %v", envelope.Error)
	}
	body, err := json.Marshal(envelope.Data)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
