package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityNormal   Priority = "normal"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

type Message struct {
	ID          string    `json:"id"`
	FromAgentID string    `json:"from_agent_id"`
	ToAgentID   *string   `json:"to_agent_id,omitempty"`
	TopicID     *string   `json:"topic_id,omitempty"`
	ToGroupID   *string   `json:"to_group_id,omitempty"`
	QueueMode   bool      `json:"queue_mode"`
	ContentType string    `json:"content_type"`
	Content     string    `json:"content"`
	Priority    Priority  `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
}

type PublishRequest struct {
	TopicID     string
	ToAgentID   string
	ToGroupID   string
	QueueMode   bool
	ReplyToID   string
	ContentType string
	Content     string
	Priority    Priority
	Tags        []string
}

type ClaimRequest struct {
	Limit        int
	TopicID      string
	FromAgentID  string
	Priority     Priority
	LeaseSeconds int
}

type ClaimedMessage struct {
	Message        Message   `json:"message"`
	ClaimToken     string    `json:"claim_token"`
	ClaimExpiresAt time.Time `json:"claim_expires_at"`
	ClaimAttempts  int       `json:"claim_attempts"`
}

type MessagesService struct {
	client *Client
}

func (s *MessagesService) Publish(ctx context.Context, req PublishRequest) (Message, error) {
	body := map[string]any{
		"content_type": req.ContentType,
		"content":      req.Content,
		"priority":     req.Priority,
		"tags":         req.Tags,
	}
	if req.TopicID != "" {
		body["topic_id"] = req.TopicID
	}
	if req.ToAgentID != "" {
		body["to_agent_id"] = req.ToAgentID
	}
	if req.ToGroupID != "" {
		body["to_group_id"] = req.ToGroupID
		body["queue_mode"] = req.QueueMode
	}
	if req.ReplyToID != "" {
		body["reply_to_id"] = req.ReplyToID
	}
	var out struct {
		Message Message `json:"message"`
	}
	if err := s.client.do(ctx, http.MethodPost, "/api/v1/messages", body, &out); err != nil {
		return Message{}, err
	}
	return out.Message, nil
}

func (s *MessagesService) MarkRead(ctx context.Context, messageID string) error {
	return s.client.do(ctx, http.MethodPost, "/api/v1/messages/"+messageID+"/read", map[string]any{}, nil)
}

func (s *MessagesService) Claim(ctx context.Context, req ClaimRequest) ([]ClaimedMessage, error) {
	body := map[string]any{
		"limit":         req.Limit,
		"lease_seconds": req.LeaseSeconds,
	}
	if req.TopicID != "" {
		body["topic_id"] = req.TopicID
	}
	if req.FromAgentID != "" {
		body["from_agent_id"] = req.FromAgentID
	}
	if req.Priority != "" {
		body["priority"] = req.Priority
	}
	var out struct {
		Claims []ClaimedMessage `json:"claims"`
	}
	if err := s.client.do(ctx, http.MethodPost, "/api/v1/messages/claim", body, &out); err != nil {
		return nil, err
	}
	return out.Claims, nil
}

func (s *MessagesService) Ack(ctx context.Context, messageID, claimToken string, markRead bool) error {
	return s.client.do(ctx, http.MethodPost, "/api/v1/messages/"+messageID+"/ack", map[string]any{
		"claim_token": claimToken,
		"mark_read":   markRead,
	}, nil)
}

func (s *MessagesService) Nack(ctx context.Context, messageID, claimToken, reason string) error {
	return s.client.do(ctx, http.MethodPost, "/api/v1/messages/"+messageID+"/nack", map[string]any{
		"claim_token": claimToken,
		"reason":      reason,
	}, nil)
}

func (s *MessagesService) Renew(ctx context.Context, messageID, claimToken string, leaseSeconds int) (time.Time, error) {
	var out struct {
		ClaimExpiresAt time.Time `json:"claim_expires_at"`
	}
	if err := s.client.do(ctx, http.MethodPost, "/api/v1/messages/"+messageID+"/renew", map[string]any{
		"claim_token":   claimToken,
		"lease_seconds": leaseSeconds,
	}, &out); err != nil {
		return time.Time{}, err
	}
	return out.ClaimExpiresAt, nil
}

func (s *MessagesService) Subscribe(ctx context.Context, topicID string) (<-chan Message, error) {
	if topicID == "" {
		return nil, fmt.Errorf("topicID is required")
	}
	u, err := url.Parse(s.client.BaseURL)
	if err != nil {
		return nil, err
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/api/v1/ws?api_key=%s", scheme, u.Host, url.QueryEscape(s.client.APIKey))
	out := make(chan Message, 64)

	go func() {
		defer close(out)
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				time.Sleep(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			backoff = time.Second
			_ = conn.WriteJSON(map[string]any{"type": "subscribe", "topic_id": topicID})
			for {
				if ctx.Err() != nil {
					_ = conn.Close()
					return
				}
				var frame map[string]any
				if err := conn.ReadJSON(&frame); err != nil {
					_ = conn.Close()
					break
				}
				if t, _ := frame["type"].(string); strings.EqualFold(t, "message") {
					raw, ok := frame["data"].(map[string]any)
					if !ok {
						continue
					}
					msg := Message{
						ID:          asString(raw["id"]),
						Content:     asString(raw["content"]),
						ContentType: asString(raw["content_type"]),
						FromAgentID: asString(raw["from_agent_id"]),
						Priority:    Priority(asString(raw["priority"])),
					}
					select {
					case out <- msg:
					case <-ctx.Done():
						_ = conn.Close()
						return
					}
				}
			}
		}
	}()
	return out, nil
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
