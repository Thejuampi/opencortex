package broker

import (
	"context"

	"opencortex/internal/model"
)

type TopicStats struct {
	TopicID      string `json:"topic_id"`
	Subscribers  int    `json:"subscribers"`
	BufferedMsgs int    `json:"buffered_messages"`
	DroppedMsgs  int64  `json:"dropped_messages"`
}

type Broker interface {
	CreateTopic(ctx context.Context, topic model.Topic) error
	DeleteTopic(ctx context.Context, topicID string) error
	Subscribe(ctx context.Context, agentID, topicID string) (<-chan model.Message, error)
	Unsubscribe(ctx context.Context, agentID, topicID string) error
	Publish(ctx context.Context, msg model.Message) error
	SendDirect(ctx context.Context, msg model.Message) error
	GetMailbox(ctx context.Context, agentID string) (<-chan model.Message, error)
	TopicStats(ctx context.Context, topicID string) (TopicStats, error)
}
