package broker

import (
	"context"

	"opencortex/internal/model"
)

type MessagePersister interface {
	PersistMessage(ctx context.Context, msg model.Message) error
}

type PersistentBroker struct {
	base      Broker
	persister MessagePersister
}

func NewPersistent(base Broker, persister MessagePersister) *PersistentBroker {
	return &PersistentBroker{base: base, persister: persister}
}

func (p *PersistentBroker) CreateTopic(ctx context.Context, topic model.Topic) error {
	return p.base.CreateTopic(ctx, topic)
}

func (p *PersistentBroker) DeleteTopic(ctx context.Context, topicID string) error {
	return p.base.DeleteTopic(ctx, topicID)
}

func (p *PersistentBroker) Subscribe(ctx context.Context, agentID, topicID string) (<-chan model.Message, error) {
	return p.base.Subscribe(ctx, agentID, topicID)
}

func (p *PersistentBroker) Unsubscribe(ctx context.Context, agentID, topicID string) error {
	return p.base.Unsubscribe(ctx, agentID, topicID)
}

func (p *PersistentBroker) Publish(ctx context.Context, msg model.Message) error {
	if p.persister != nil {
		if err := p.persister.PersistMessage(ctx, msg); err != nil {
			return err
		}
	}
	return p.base.Publish(ctx, msg)
}

func (p *PersistentBroker) SendDirect(ctx context.Context, msg model.Message) error {
	if p.persister != nil {
		if err := p.persister.PersistMessage(ctx, msg); err != nil {
			return err
		}
	}
	return p.base.SendDirect(ctx, msg)
}

func (p *PersistentBroker) GetMailbox(ctx context.Context, agentID string) (<-chan model.Message, error) {
	return p.base.GetMailbox(ctx, agentID)
}

func (p *PersistentBroker) TopicStats(ctx context.Context, topicID string) (TopicStats, error) {
	return p.base.TopicStats(ctx, topicID)
}
