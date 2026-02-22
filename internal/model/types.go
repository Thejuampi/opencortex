package model

import "time"

type AgentType string

const (
	AgentTypeHuman  AgentType = "human"
	AgentTypeAI     AgentType = "ai"
	AgentTypeSystem AgentType = "system"
)

type AgentStatus string

const (
	AgentStatusActive   AgentStatus = "active"
	AgentStatusInactive AgentStatus = "inactive"
	AgentStatusBanned   AgentStatus = "banned"
)

type RoleName string

const (
	RoleAdmin    RoleName = "admin"
	RoleAgent    RoleName = "agent"
	RoleReadonly RoleName = "readonly"
	RoleSync     RoleName = "sync"
)

type Agent struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        AgentType      `json:"type"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags"`
	Status      AgentStatus    `json:"status"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
	LastSeen    *time.Time     `json:"last_seen,omitempty"`
}

type TopicRetention string

const (
	TopicRetentionNone       TopicRetention = "none"
	TopicRetentionPersistent TopicRetention = "persistent"
	TopicRetentionTTL        TopicRetention = "ttl"
)

type Topic struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Retention   TopicRetention `json:"retention"`
	TTLSeconds  *int           `json:"ttl_seconds,omitempty"`
	CreatedBy   string         `json:"created_by"`
	IsPublic    bool           `json:"is_public"`
	CreatedAt   time.Time      `json:"created_at"`
}

type MessageStatus string

const (
	MessageStatusPending   MessageStatus = "pending"
	MessageStatusDelivered MessageStatus = "delivered"
	MessageStatusRead      MessageStatus = "read"
	MessageStatusExpired   MessageStatus = "expired"
)

type MessagePriority string

const (
	MessagePriorityLow      MessagePriority = "low"
	MessagePriorityNormal   MessagePriority = "normal"
	MessagePriorityHigh     MessagePriority = "high"
	MessagePriorityCritical MessagePriority = "critical"
)

type Message struct {
	ID          string          `json:"id"`
	FromAgentID string          `json:"from_agent_id"`
	ToAgentID   *string         `json:"to_agent_id,omitempty"`
	TopicID     *string         `json:"topic_id,omitempty"`
	ToGroupID   *string         `json:"to_group_id,omitempty"`
	QueueMode   bool            `json:"queue_mode"`
	ReplyToID   *string         `json:"reply_to_id,omitempty"`
	ContentType string          `json:"content_type"`
	Content     string          `json:"content"`
	Status      MessageStatus   `json:"status"`
	Priority    MessagePriority `json:"priority"`
	Tags        []string        `json:"tags"`
	Metadata    map[string]any  `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
	DeliveredAt *time.Time      `json:"delivered_at,omitempty"`
	ReadAt      *time.Time      `json:"read_at,omitempty"`
}

type Subscription struct {
	AgentID   string         `json:"agent_id"`
	TopicID   string         `json:"topic_id"`
	Filter    map[string]any `json:"filter,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type GroupMode string

const (
	GroupModeFanout GroupMode = "fanout"
	GroupModeQueue  GroupMode = "queue"
)

type Group struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Mode        GroupMode      `json:"mode"`
	CreatedBy   string         `json:"created_by"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
}

type GroupMember struct {
	GroupID  string    `json:"group_id"`
	AgentID  string    `json:"agent_id"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
	Agent    Agent     `json:"agent"`
}

type KnowledgeVisibility string

const (
	KnowledgeVisibilityPublic     KnowledgeVisibility = "public"
	KnowledgeVisibilityRestricted KnowledgeVisibility = "restricted"
)

type KnowledgeEntry struct {
	ID           string              `json:"id"`
	Title        string              `json:"title"`
	Content      string              `json:"content"`
	ContentType  string              `json:"content_type"`
	Summary      *string             `json:"summary,omitempty"`
	Tags         []string            `json:"tags"`
	CollectionID *string             `json:"collection_id,omitempty"`
	CreatedBy    string              `json:"created_by"`
	UpdatedBy    string              `json:"updated_by"`
	Version      int                 `json:"version"`
	Checksum     string              `json:"checksum"`
	IsPinned     bool                `json:"is_pinned"`
	Visibility   KnowledgeVisibility `json:"visibility"`
	Source       *string             `json:"source,omitempty"`
	Metadata     map[string]any      `json:"metadata"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type KnowledgeVersion struct {
	ID          string    `json:"id"`
	KnowledgeID string    `json:"knowledge_id"`
	Version     int       `json:"version"`
	Content     string    `json:"content"`
	Summary     *string   `json:"summary,omitempty"`
	ChangedBy   string    `json:"changed_by"`
	ChangeNote  *string   `json:"change_note,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Collection struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ParentID    *string        `json:"parent_id,omitempty"`
	CreatedBy   string         `json:"created_by"`
	IsPublic    bool           `json:"is_public"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type SyncDirection string

const (
	SyncDirectionPush          SyncDirection = "push"
	SyncDirectionPull          SyncDirection = "pull"
	SyncDirectionBidirectional SyncDirection = "bidirectional"
)

type SyncScope string

const (
	SyncScopeFull        SyncScope = "full"
	SyncScopeCollections SyncScope = "collections"
	SyncScopeTopics      SyncScope = "topics"
	SyncScopeMessages    SyncScope = "messages"
)

type SyncManifest struct {
	ID         string        `json:"id"`
	RemoteURL  string        `json:"remote_url"`
	RemoteName string        `json:"remote_name"`
	Direction  SyncDirection `json:"direction"`
	Scope      SyncScope     `json:"scope"`
	ScopeIDs   []string      `json:"scope_ids"`
	LastSyncAt *time.Time    `json:"last_sync_at,omitempty"`
	LastSyncOK *bool         `json:"last_sync_ok,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
}

type SyncStatus string

const (
	SyncStatusRunning SyncStatus = "running"
	SyncStatusSuccess SyncStatus = "success"
	SyncStatusPartial SyncStatus = "partial"
	SyncStatusFailed  SyncStatus = "failed"
)

type SyncLog struct {
	ID           string        `json:"id"`
	ManifestID   string        `json:"manifest_id"`
	Direction    SyncDirection `json:"direction"`
	Status       SyncStatus    `json:"status"`
	ItemsPushed  int           `json:"items_pushed"`
	ItemsPulled  int           `json:"items_pulled"`
	Conflicts    int           `json:"conflicts"`
	ErrorMessage *string       `json:"error_message,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
}

type SyncConflict struct {
	ID             string     `json:"id"`
	ManifestID     string     `json:"manifest_id"`
	EntityType     string     `json:"entity_type"`
	EntityID       string     `json:"entity_id"`
	LocalChecksum  string     `json:"local_checksum"`
	RemoteChecksum string     `json:"remote_checksum"`
	Strategy       string     `json:"strategy"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
}

type Permission struct {
	ID          string `json:"id"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Description string `json:"description"`
}

type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
}

type AuditLog struct {
	ID         string         `json:"id"`
	AgentID    *string        `json:"agent_id,omitempty"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	ResourceID *string        `json:"resource_id,omitempty"`
	Metadata   map[string]any `json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
}
