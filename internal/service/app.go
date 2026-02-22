package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"opencortex/internal/auth"
	"opencortex/internal/broker"
	"opencortex/internal/config"
	"opencortex/internal/model"
	"opencortex/internal/storage/repos"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not_found")
	ErrConflict     = errors.New("conflict")
	ErrValidation   = errors.New("validation")
)

type AuthContext struct {
	Agent      model.Agent
	Roles      []string
	Permission map[string]struct{}
}

type App struct {
	Config        config.Config
	Store         *repos.Store
	Broker        broker.Broker
	KnowledgeSink chan model.KnowledgeEntry
	AgentSink     chan model.Agent
}

func New(cfg config.Config, store *repos.Store, broker broker.Broker) *App {
	return &App{
		Config:        cfg,
		Store:         store,
		Broker:        broker,
		KnowledgeSink: make(chan model.KnowledgeEntry, 256),
		AgentSink:     make(chan model.Agent, 256),
	}
}

func (a *App) BootstrapInit(ctx context.Context, adminName string) (model.Agent, string, error) {
	if strings.TrimSpace(adminName) == "" {
		adminName = "admin"
	}
	if err := a.Store.SeedRBAC(ctx); err != nil {
		return model.Agent{}, "", err
	}
	raw, hash, err := auth.GenerateAPIKey("live")
	if err != nil {
		return model.Agent{}, "", err
	}
	agentID := uuid.NewString()
	agent, err := a.Store.CreateAgent(ctx, repos.CreateAgentInput{
		ID:          agentID,
		Name:        adminName,
		Type:        model.AgentTypeHuman,
		APIKeyHash:  hash,
		Description: "Initial admin agent",
		Status:      model.AgentStatusActive,
		Tags:        []string{"admin"},
		Metadata:    map[string]any{"bootstrap": true},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return model.Agent{}, "", fmt.Errorf("%w: admin already exists", ErrConflict)
		}
		return model.Agent{}, "", err
	}
	if err := a.Store.AssignRole(ctx, agent.ID, string(model.RoleAdmin)); err != nil {
		return model.Agent{}, "", err
	}
	return agent, raw, nil
}

func (a *App) Authenticate(ctx context.Context, rawKey string) (AuthContext, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return AuthContext{}, ErrUnauthorized
	}
	hash := auth.HashKey(rawKey)
	agent, err := a.Store.GetAgentByAPIKeyHash(ctx, hash)
	if err != nil {
		if err == sql.ErrNoRows {
			return AuthContext{}, ErrUnauthorized
		}
		return AuthContext{}, err
	}
	if agent.Status != model.AgentStatusActive {
		return AuthContext{}, ErrForbidden
	}
	roles, err := a.Store.AgentRoles(ctx, agent.ID)
	if err != nil {
		return AuthContext{}, err
	}
	if len(roles) == 0 {
		_ = a.Store.EnsureRoleAssignment(ctx, agent.ID, string(model.RoleAgent))
		roles, _ = a.Store.AgentRoles(ctx, agent.ID)
	}
	perms, err := a.Store.AgentPermissionSet(ctx, agent.ID)
	if err != nil {
		return AuthContext{}, err
	}
	_ = a.Store.UpdateLastSeen(ctx, agent.ID)
	return AuthContext{
		Agent:      agent,
		Roles:      roles,
		Permission: perms,
	}, nil
}

func (a *App) Authorize(authCtx AuthContext, resource, action string) error {
	if auth.IsAllowed(authCtx.Roles, resource, action, authCtx.Permission) {
		return nil
	}
	return ErrForbidden
}

func (a *App) CreateAgent(ctx context.Context, in repos.CreateAgentInput, keyKind string, assignRole string) (model.Agent, string, error) {
	raw, hash, err := auth.GenerateAPIKey(keyKind)
	if err != nil {
		return model.Agent{}, "", err
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	in.APIKeyHash = hash
	agent, err := a.Store.CreateAgent(ctx, in)
	if err != nil {
		return model.Agent{}, "", err
	}
	role := assignRole
	if role == "" {
		role = string(model.RoleAgent)
	}
	if err := a.Store.AssignRole(ctx, agent.ID, role); err != nil {
		return model.Agent{}, "", err
	}
	select {
	case a.AgentSink <- agent:
	default:
	}
	return agent, raw, nil
}

func (a *App) RotateAgentKey(ctx context.Context, agentID, kind string) (string, error) {
	raw, hash, err := auth.GenerateAPIKey(kind)
	if err != nil {
		return "", err
	}
	if err := a.Store.RotateAgentKey(ctx, agentID, hash); err != nil {
		return "", err
	}
	return raw, nil
}

func (a *App) CreateTopic(ctx context.Context, in repos.CreateTopicInput) (model.Topic, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	topic, err := a.Store.CreateTopic(ctx, in)
	if err != nil {
		return model.Topic{}, err
	}
	_ = a.Broker.CreateTopic(ctx, topic)
	return topic, nil
}

func (a *App) CreateMessage(ctx context.Context, in repos.CreateMessageInput) (model.Message, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if in.TopicID == nil && in.ToAgentID == nil {
		return model.Message{}, fmt.Errorf("%w: to_agent_id or topic_id required", ErrValidation)
	}

	var recipients []string
	if in.ToAgentID != nil {
		recipients = append(recipients, *in.ToAgentID)
	}
	if in.TopicID != nil {
		topicRecipients, err := a.Store.RecipientsForTopic(ctx, *in.TopicID)
		if err != nil {
			return model.Message{}, err
		}
		recipients = append(recipients, topicRecipients...)
	}
	msg, err := a.Store.CreateMessageWithRecipients(ctx, in, recipients)
	if err != nil {
		return model.Message{}, err
	}
	if msg.TopicID != nil {
		_ = a.Broker.Publish(ctx, msg)
	}
	if msg.ToAgentID != nil {
		_ = a.Broker.SendDirect(ctx, msg)
	}
	return msg, nil
}

func (a *App) CreateKnowledge(ctx context.Context, in repos.CreateKnowledgeInput) (model.KnowledgeEntry, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	entry, err := a.Store.CreateKnowledge(ctx, in)
	if err != nil {
		return model.KnowledgeEntry{}, err
	}
	select {
	case a.KnowledgeSink <- entry:
	default:
	}
	return entry, nil
}

func (a *App) UpdateKnowledgeContent(ctx context.Context, in repos.UpdateKnowledgeContentInput) (model.KnowledgeEntry, error) {
	entry, err := a.Store.UpdateKnowledgeContent(ctx, in)
	if err != nil {
		return model.KnowledgeEntry{}, err
	}
	select {
	case a.KnowledgeSink <- entry:
	default:
	}
	return entry, nil
}

func (a *App) CreateCollection(ctx context.Context, in repos.CreateCollectionInput) (model.Collection, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	return a.Store.CreateCollection(ctx, in)
}

func (a *App) AddRemote(ctx context.Context, in repos.CreateRemoteInput, rawAPIKey string) (model.SyncManifest, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if rawAPIKey != "" {
		in.APIKeyHash = auth.HashKey(rawAPIKey)
	}
	return a.Store.CreateRemote(ctx, in)
}

func parseExpiresIn(seconds int) *time.Time {
	if seconds <= 0 {
		return nil
	}
	t := nowUTC().Add(time.Duration(seconds) * time.Second)
	return &t
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
