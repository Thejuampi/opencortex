package broker

import (
	"context"
	"testing"
	"time"

	"opencortex/internal/model"
)

func TestPublishSubscribe(t *testing.T) {
	b := NewMemory(4)
	topic := model.Topic{ID: "topic-1"}
	if err := b.CreateTopic(context.Background(), topic); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	ch, err := b.Subscribe(context.Background(), "agent-1", topic.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	msg := model.Message{
		ID:      "m1",
		TopicID: &topic.ID,
		Content: "hello",
	}
	if err := b.Publish(context.Background(), msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case got := <-ch:
		if got.ID != msg.ID {
			t.Fatalf("unexpected message id: %s", got.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}
