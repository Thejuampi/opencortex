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
	"opencortex/internal/knowledge"
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

const (
	SystemBroadcastTopicID   = "system.broadcast"
	SystemBroadcastTopicName = "system.broadcast"
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
	if _, err := a.EnsureBroadcastSetup(ctx, agent.ID); err != nil {
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
	topic, err := a.EnsureBroadcastSetup(ctx, agent.ID)
	if err == nil {
		_ = a.Store.Subscribe(ctx, agent.ID, topic.ID, nil)
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
	if in.QueueMode && in.ToGroupID == nil {
		return model.Message{}, fmt.Errorf("%w: queue_mode requires to_group_id", ErrValidation)
	}
	if in.TopicID == nil && in.ToAgentID == nil && in.ToGroupID == nil {
		return model.Message{}, fmt.Errorf("%w: to_agent_id or topic_id or to_group_id required", ErrValidation)
	}

	var recipients []string
	var groupMembers []string
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
	if in.ToGroupID != nil {
		group, err := a.Store.GetGroupByID(ctx, *in.ToGroupID)
		if err != nil {
			if err == sql.ErrNoRows {
				return model.Message{}, fmt.Errorf("%w: group not found", ErrNotFound)
			}
			return model.Message{}, err
		}
		groupMembers, err = a.Store.ListGroupMemberIDs(ctx, group.ID)
		if err != nil {
			return model.Message{}, err
		}
		if len(groupMembers) == 0 {
			return model.Message{}, fmt.Errorf("%w: group has no members", ErrValidation)
		}
		if in.QueueMode && group.Mode != model.GroupModeQueue {
			return model.Message{}, fmt.Errorf("%w: queue_mode requires group mode queue", ErrValidation)
		}
		if group.Mode == model.GroupModeQueue {
			in.QueueMode = true
		}
		if !in.QueueMode {
			recipients = append(recipients, groupMembers...)
		}
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
	if msg.ToGroupID != nil {
		for _, memberID := range groupMembers {
			memberID := memberID
			direct := msg
			direct.ToAgentID = &memberID
			_ = a.Broker.SendDirect(ctx, direct)
		}
	}
	return msg, nil
}

func (a *App) CreateBroadcastMessage(ctx context.Context, in repos.CreateMessageInput) (model.Message, error) {
	topic, err := a.EnsureBroadcastSetup(ctx, in.FromAgentID)
	if err != nil {
		return model.Message{}, err
	}
	in.TopicID = &topic.ID
	in.ToAgentID = nil
	in.ToGroupID = nil
	in.QueueMode = false
	return a.CreateMessage(ctx, in)
}

func (a *App) CreateKnowledge(ctx context.Context, in repos.CreateKnowledgeInput) (model.KnowledgeEntry, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if in.Summary == nil || strings.TrimSpace(*in.Summary) == "" {
		auto := knowledge.GenerateAbstract(in.Content, 280)
		if auto != "" {
			in.Summary = &auto
		}
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
	if in.Summary == nil || strings.TrimSpace(*in.Summary) == "" {
		auto := knowledge.GenerateAbstract(in.Content, 280)
		if auto != "" {
			in.Summary = &auto
		}
	}
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

func (a *App) EnsureBroadcastSetup(ctx context.Context, createdBy string) (model.Topic, error) {
	topic, err := a.Store.GetTopicByID(ctx, SystemBroadcastTopicID)
	if err != nil {
		if err != sql.ErrNoRows {
			return model.Topic{}, err
		}
		if strings.TrimSpace(createdBy) == "" {
			var fallbackErr error
			createdBy, fallbackErr = a.firstAgentID(ctx)
			if fallbackErr != nil {
				return model.Topic{}, fallbackErr
			}
		}
		topic, err = a.CreateTopic(ctx, repos.CreateTopicInput{
			ID:          SystemBroadcastTopicID,
			Name:        SystemBroadcastTopicName,
			Description: "System-wide broadcast channel",
			Retention:   model.TopicRetentionPersistent,
			CreatedBy:   createdBy,
			IsPublic:    true,
		})
		if err != nil {
			return model.Topic{}, err
		}
	}
	_ = a.Broker.CreateTopic(ctx, topic)
	_ = a.Store.SubscribeAllAgents(ctx, topic.ID)
	return topic, nil
}

func (a *App) firstAgentID(ctx context.Context) (string, error) {
	agents, _, err := a.Store.ListAgents(ctx, "", "", 1, 1)
	if err != nil {
		return "", err
	}
	if len(agents) == 0 {
		return "", fmt.Errorf("%w: no agents available to create broadcast topic", ErrNotFound)
	}
	return agents[0].ID, nil
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
