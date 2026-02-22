package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	gws "github.com/gorilla/websocket"

	"opencortex/internal/model"
	"opencortex/internal/service"
	"opencortex/internal/storage/repos"
)

type Hub struct {
	App      *service.App
	Store    *repos.Store
	upgrader gws.Upgrader

	mu      sync.RWMutex
	clients map[*client]struct{}
}

type client struct {
	conn        *gws.Conn
	app         *service.App
	store       *repos.Store
	auth        service.AuthContext
	writeMu     sync.Mutex
	topicCancel map[string]context.CancelFunc
	mailboxStop context.CancelFunc
}

func NewHub(app *service.App, store *repos.Store) *Hub {
	return &Hub{
		App:   app,
		Store: store,
		upgrader: gws.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: map[*client]struct{}{},
	}
}

func (h *Hub) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case entry := <-h.App.KnowledgeSink:
				h.broadcast(map[string]any{
					"type": "knowledge_updated",
					"data": map[string]any{
						"id":    entry.ID,
						"title": entry.Title,
					},
				})
			case agent := <-h.App.AgentSink:
				h.broadcast(map[string]any{
					"type": "agent_status",
					"data": map[string]any{
						"agent_id": agent.ID,
						"status":   agent.Status,
					},
				})
			}
		}
	}()
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	apiKey := r.URL.Query().Get("api_key")
	authCtx, err := h.App.Authenticate(r.Context(), apiKey)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{
		conn:        conn,
		app:         h.App,
		store:       h.Store,
		auth:        authCtx,
		topicCancel: map[string]context.CancelFunc{},
	}
	h.register(c)
	defer h.unregister(c)
	defer conn.Close()

	_ = c.write(map[string]any{"type": "ack", "ok": true, "ref_id": "connected"})
	c.startMailbox()
	_ = c.subscribeTopic(service.SystemBroadcastTopicID, "")
	for {
		_, b, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req map[string]any
		if err := json.Unmarshal(b, &req); err != nil {
			_ = c.write(map[string]any{"type": "error", "code": "BAD_PAYLOAD", "message": "invalid JSON"})
			continue
		}
		msgType, _ := req["type"].(string)
		switch msgType {
		case "ping":
			_ = c.write(map[string]any{"type": "pong"})
		case "subscribe":
			topicID, _ := req["topic_id"].(string)
			cursor, _ := req["cursor"].(string)
			if topicID == "" {
				_ = c.write(map[string]any{"type": "error", "code": "VALIDATION_ERROR", "message": "topic_id required"})
				continue
			}
			if err := c.subscribeTopic(topicID, cursor); err != nil {
				_ = c.write(map[string]any{"type": "error", "code": "SUBSCRIBE_FAILED", "message": err.Error()})
				continue
			}
			_ = c.write(map[string]any{"type": "ack", "ok": true, "ref_id": topicID})
		case "unsubscribe":
			topicID, _ := req["topic_id"].(string)
			c.unsubscribeTopic(topicID)
			_ = c.write(map[string]any{"type": "ack", "ok": true, "ref_id": topicID})
		case "send":
			rawPayload, ok := req["payload"].(map[string]any)
			if !ok {
				_ = c.write(map[string]any{"type": "error", "code": "VALIDATION_ERROR", "message": "payload required"})
				continue
			}
			msg, err := c.send(rawPayload)
			if err != nil {
				_ = c.write(map[string]any{"type": "error", "code": "SEND_FAILED", "message": err.Error()})
				continue
			}
			_ = c.write(map[string]any{"type": "ack", "ok": true, "data": map[string]any{"id": msg.ID}})
		default:
			_ = c.write(map[string]any{"type": "error", "code": "UNKNOWN_TYPE", "message": "unsupported message type"})
		}
	}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.mailboxStop != nil {
		c.mailboxStop()
	}
	for _, cancel := range c.topicCancel {
		cancel()
	}
	delete(h.clients, c)
}

func (h *Hub) broadcast(msg map[string]any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		_ = c.write(msg)
	}
}

func (c *client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(v)
}

func (c *client) subscribeTopic(topicID, cursor string) error {
	if _, exists := c.topicCancel[topicID]; exists {
		return nil
	}
	if err := c.store.Subscribe(context.Background(), c.auth.Agent.ID, topicID, nil); err != nil {
		return err
	}
	ch, err := c.app.Broker.Subscribe(context.Background(), c.auth.Agent.ID, topicID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.topicCancel[topicID] = cancel

	msgs, newCursor, _ := c.app.GetInboxAsync(ctx, c.auth.Agent.ID, cursor, repos.GetInboxFilters{TopicID: topicID, Limit: 100})
	if msgs == nil {
		msgs = []model.Message{}
	}
	_ = c.write(map[string]any{
		"type":     "initial_image",
		"topic_id": topicID,
		"cursor":   newCursor,
		"messages": msgs,
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				_ = c.write(map[string]any{
					"type":     "delta",
					"topic_id": topicID,
					"data":     messageHint(msg),
				})
				_ = c.write(map[string]any{"type": "message", "data": msg})
			}
		}
	}()
	return nil
}

func (c *client) unsubscribeTopic(topicID string) {
	if cancel, ok := c.topicCancel[topicID]; ok {
		cancel()
		delete(c.topicCancel, topicID)
	}
	_ = c.store.Unsubscribe(context.Background(), c.auth.Agent.ID, topicID)
	_ = c.app.Broker.Unsubscribe(context.Background(), c.auth.Agent.ID, topicID)
}

func (c *client) send(payload map[string]any) (model.Message, error) {
	var (
		toAgentID *string
		topicID   *string
		replyToID *string
	)
	if v, ok := payload["to_agent_id"].(string); ok && v != "" {
		toAgentID = &v
	}
	if v, ok := payload["topic_id"].(string); ok && v != "" {
		topicID = &v
	}
	if v, ok := payload["reply_to_id"].(string); ok && v != "" {
		replyToID = &v
	}
	contentType, _ := payload["content_type"].(string)
	if contentType == "" {
		contentType = "text/plain"
	}
	content, _ := payload["content"].(string)
	priority, _ := payload["priority"].(string)
	msg, err := c.app.CreateMessage(context.Background(), repos.CreateMessageInput{
		FromAgentID: c.auth.Agent.ID,
		ToAgentID:   toAgentID,
		TopicID:     topicID,
		ReplyToID:   replyToID,
		ContentType: contentType,
		Content:     content,
		Priority:    model.MessagePriority(priority),
	})
	if err != nil {
		return model.Message{}, err
	}
	return msg, nil
}

func (c *client) startMailbox() {
	if c.mailboxStop != nil {
		return
	}
	ch, err := c.app.Broker.GetMailbox(context.Background(), c.auth.Agent.ID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mailboxStop = cancel
	msgs, newCursor, _ := c.app.GetInboxAsync(ctx, c.auth.Agent.ID, "", repos.GetInboxFilters{Limit: 100})
	if msgs == nil {
		msgs = []model.Message{}
	}
	_ = c.write(map[string]any{
		"type":     "initial_image",
		"topic_id": "",
		"cursor":   newCursor,
		"messages": msgs,
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				_ = c.write(map[string]any{
					"type":     "delta",
					"topic_id": "",
					"data":     messageHint(msg),
				})
				_ = c.write(map[string]any{"type": "message", "data": msg})
			}
		}
	}()
}

func messageHint(msg model.Message) map[string]any {
	return map[string]any{
		"id":            msg.ID,
		"from_agent_id": msg.FromAgentID,
		"to_agent_id":   msg.ToAgentID,
		"topic_id":      msg.TopicID,
		"priority":      msg.Priority,
		"reply_to_id":   msg.ReplyToID,
		"metadata":      msg.Metadata,
		"created_at":    msg.CreatedAt,
	}
}
