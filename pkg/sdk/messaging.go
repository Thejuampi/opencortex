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
	ContentType string    `json:"content_type"`
	Content     string    `json:"content"`
	Priority    Priority  `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
}

type PublishRequest struct {
	TopicID     string
	ToAgentID   string
	ReplyToID   string
	ContentType string
	Content     string
	Priority    Priority
	Tags        []string
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
