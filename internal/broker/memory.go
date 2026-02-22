package broker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"opencortex/internal/model"
)

type topicState struct {
	id      string
	subs    map[string]chan model.Message
	dropped atomic.Int64
}

type MemoryBroker struct {
	mu          sync.RWMutex
	topics      map[string]*topicState
	direct      map[string]chan model.Message
	bufferSize  int
	defaultMail int
}

func NewMemory(bufferSize int) *MemoryBroker {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	return &MemoryBroker{
		topics:      map[string]*topicState{},
		direct:      map[string]chan model.Message{},
		bufferSize:  bufferSize,
		defaultMail: bufferSize,
	}
}

func (b *MemoryBroker) CreateTopic(_ context.Context, topic model.Topic) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.topics[topic.ID]; ok {
		return nil
	}
	b.topics[topic.ID] = &topicState{
		id:   topic.ID,
		subs: map[string]chan model.Message{},
	}
	return nil
}

func (b *MemoryBroker) DeleteTopic(_ context.Context, topicID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.topics[topicID]
	if !ok {
		return nil
	}
	for _, ch := range t.subs {
		close(ch)
	}
	delete(b.topics, topicID)
	return nil
}

func (b *MemoryBroker) Subscribe(_ context.Context, agentID, topicID string) (<-chan model.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.topics[topicID]
	if !ok {
		t = &topicState{id: topicID, subs: map[string]chan model.Message{}}
		b.topics[topicID] = t
	}
	if ch, ok := t.subs[agentID]; ok {
		return ch, nil
	}
	ch := make(chan model.Message, b.bufferSize)
	t.subs[agentID] = ch
	return ch, nil
}

func (b *MemoryBroker) Unsubscribe(_ context.Context, agentID, topicID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.topics[topicID]
	if !ok {
		return nil
	}
	if ch, ok := t.subs[agentID]; ok {
		close(ch)
		delete(t.subs, agentID)
	}
	return nil
}

func (b *MemoryBroker) Publish(_ context.Context, msg model.Message) error {
	if msg.TopicID == nil || *msg.TopicID == "" {
		return fmt.Errorf("missing topic_id")
	}
	b.mu.RLock()
	t, ok := b.topics[*msg.TopicID]
	b.mu.RUnlock()
	if !ok {
		return nil
	}
	for _, ch := range t.subs {
		select {
		case ch <- msg:
		default:
			t.dropped.Add(1)
		}
	}
	return nil
}

func (b *MemoryBroker) SendDirect(_ context.Context, msg model.Message) error {
	if msg.ToAgentID == nil || *msg.ToAgentID == "" {
		return fmt.Errorf("missing to_agent_id")
	}
	b.mu.Lock()
	ch, ok := b.direct[*msg.ToAgentID]
	if !ok {
		ch = make(chan model.Message, b.defaultMail)
		b.direct[*msg.ToAgentID] = ch
	}
	b.mu.Unlock()
	select {
	case ch <- msg:
	default:
	}
	return nil
}

func (b *MemoryBroker) GetMailbox(_ context.Context, agentID string) (<-chan model.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.direct[agentID]; ok {
		return ch, nil
	}
	ch := make(chan model.Message, b.defaultMail)
	b.direct[agentID] = ch
	return ch, nil
}

func (b *MemoryBroker) TopicStats(_ context.Context, topicID string) (TopicStats, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	t, ok := b.topics[topicID]
	if !ok {
		return TopicStats{TopicID: topicID}, nil
	}
	buffered := 0
	for _, ch := range t.subs {
		buffered += len(ch)
	}
	return TopicStats{
		TopicID:      topicID,
		Subscribers:  len(t.subs),
		BufferedMsgs: buffered,
		DroppedMsgs:  t.dropped.Load(),
	}, nil
}
