package repos

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"opencortex/internal/config"
	"opencortex/internal/model"
	"opencortex/internal/storage"
)

func TestMessageClaimLifecycle(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupMessageClaimStore(t)
	defer cleanup()

	sender := createTestAgent(t, ctx, store, "sender")
	recipient := createTestAgent(t, ctx, store, "recipient")

	messageID := createDirectMessage(t, ctx, store, sender.ID, recipient.ID, "hello")

	claims, err := store.ClaimMessages(ctx, ClaimMessagesInput{
		AgentID:      recipient.ID,
		Limit:        1,
		LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("claim messages: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	if claims[0].Message.ID != messageID {
		t.Fatalf("unexpected message claimed: %s", claims[0].Message.ID)
	}

	if err := store.AckMessageClaim(ctx, messageID, recipient.ID, claims[0].ClaimToken, true); err != nil {
		t.Fatalf("ack claim: %v", err)
	}

	inbox, _, err := store.ListInbox(ctx, recipient.ID, MessageFilters{Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("expected 1 inbox message, got %d", len(inbox))
	}
	if inbox[0].Status != model.MessageStatusRead {
		t.Fatalf("expected read status, got %s", inbox[0].Status)
	}
}

func TestMessageClaimConcurrencySingleWinner(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupMessageClaimStore(t)
	defer cleanup()

	sender := createTestAgent(t, ctx, store, "sender")
	recipient := createTestAgent(t, ctx, store, "recipient")
	_ = createDirectMessage(t, ctx, store, sender.ID, recipient.ID, "work-item")

	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			claims, err := store.ClaimMessages(ctx, ClaimMessagesInput{
				AgentID:      recipient.ID,
				Limit:        1,
				LeaseSeconds: 30,
			})
			errs[i] = err
			results[i] = len(claims)
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("claim error: %v", err)
		}
	}
	if results[0]+results[1] != 1 {
		t.Fatalf("expected single winner, got claim counts %v", results)
	}
}

func TestMessageClaimNackAndReclaim(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupMessageClaimStore(t)
	defer cleanup()

	sender := createTestAgent(t, ctx, store, "sender")
	recipient := createTestAgent(t, ctx, store, "recipient")
	messageID := createDirectMessage(t, ctx, store, sender.ID, recipient.ID, "retry-me")

	claims, err := store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: recipient.ID, Limit: 1, LeaseSeconds: 30})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}

	if err := store.NackMessageClaim(ctx, messageID, recipient.ID, claims[0].ClaimToken, "temporary failure"); err != nil {
		t.Fatalf("nack: %v", err)
	}

	reclaim, err := store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: recipient.ID, Limit: 1, LeaseSeconds: 30})
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaim) != 1 {
		t.Fatalf("expected reclaim, got %d", len(reclaim))
	}
	if reclaim[0].ClaimToken == claims[0].ClaimToken {
		t.Fatalf("expected new claim token")
	}
}

func TestMessageClaimAckInvalidOrExpired(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupMessageClaimStore(t)
	defer cleanup()

	sender := createTestAgent(t, ctx, store, "sender")
	recipient := createTestAgent(t, ctx, store, "recipient")
	messageID := createDirectMessage(t, ctx, store, sender.ID, recipient.ID, "expiring")

	claims, err := store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: recipient.ID, Limit: 1, LeaseSeconds: 1})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected claim")
	}

	if err := store.AckMessageClaim(ctx, messageID, recipient.ID, "wrong-token", true); err == nil {
		t.Fatalf("expected error for wrong token")
	}

	time.Sleep(1100 * time.Millisecond)

	if err := store.AckMessageClaim(ctx, messageID, recipient.ID, claims[0].ClaimToken, true); err == nil {
		t.Fatalf("expected error for expired token")
	}
}

func TestMessageClaimRenew(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupMessageClaimStore(t)
	defer cleanup()

	sender := createTestAgent(t, ctx, store, "sender")
	recipient := createTestAgent(t, ctx, store, "recipient")
	messageID := createDirectMessage(t, ctx, store, sender.ID, recipient.ID, "renewable")

	claims, err := store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: recipient.ID, Limit: 1, LeaseSeconds: 1})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected claim")
	}

	renewedUntil, err := store.RenewMessageClaim(ctx, messageID, recipient.ID, claims[0].ClaimToken, 10)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewedUntil.After(time.Now().UTC().Add(5 * time.Second)) {
		t.Fatalf("expected extended lease, got %s", renewedUntil)
	}
}

func TestMessageClaimQueueGroupSingleWinner(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupMessageClaimStore(t)
	defer cleanup()

	sender := createTestAgent(t, ctx, store, "sender")
	workerA := createTestAgent(t, ctx, store, "worker-a")
	workerB := createTestAgent(t, ctx, store, "worker-b")

	group, err := store.CreateGroup(ctx, CreateGroupInput{
		ID:          newID(),
		Name:        "queue-reviewers",
		Description: "queue group",
		Mode:        model.GroupModeQueue,
		CreatedBy:   sender.ID,
		Metadata:    map[string]any{},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := store.AddGroupMember(ctx, group.ID, workerA.ID, "member"); err != nil {
		t.Fatalf("add member a: %v", err)
	}
	if err := store.AddGroupMember(ctx, group.ID, workerB.ID, "member"); err != nil {
		t.Fatalf("add member b: %v", err)
	}

	msgID := newID()
	groupID := group.ID
	_, err = store.CreateMessageWithRecipients(ctx, CreateMessageInput{
		ID:          msgID,
		FromAgentID: sender.ID,
		ToGroupID:   &groupID,
		QueueMode:   true,
		ContentType: "text/plain",
		Content:     "queue task",
		Priority:    model.MessagePriorityNormal,
		Metadata:    map[string]any{},
	}, nil)
	if err != nil {
		t.Fatalf("create queue message: %v", err)
	}

	claimsA, err := store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: workerA.ID, Limit: 1, LeaseSeconds: 1})
	if err != nil {
		t.Fatalf("claim by worker a: %v", err)
	}
	claimsB, err := store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: workerB.ID, Limit: 1, LeaseSeconds: 1})
	if err != nil {
		t.Fatalf("claim by worker b: %v", err)
	}

	gotA := len(claimsA) == 1 && claimsA[0].Message.ID == msgID
	gotB := len(claimsB) == 1 && claimsB[0].Message.ID == msgID
	if gotA == gotB {
		t.Fatalf("expected exactly one worker to claim queue task, got A=%v B=%v", gotA, gotB)
	}

	time.Sleep(1100 * time.Millisecond)
	var retry []ClaimedMessage
	if gotA {
		retry, err = store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: workerB.ID, Limit: 1, LeaseSeconds: 60})
	} else {
		retry, err = store.ClaimMessages(ctx, ClaimMessagesInput{AgentID: workerA.ID, Limit: 1, LeaseSeconds: 60})
	}
	if err != nil {
		t.Fatalf("retry claim after expiry: %v", err)
	}
	if len(retry) != 1 || retry[0].Message.ID != msgID {
		t.Fatalf("expected retry claim by other worker after lease expiry")
	}
}

func setupMessageClaimStore(t *testing.T) (*Store, func()) {
	t.Helper()

	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "claim-test.db")

	db, err := storage.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate db: %v", err)
	}

	store := New(db)
	return store, func() {
		_ = db.Close()
	}
}

func createTestAgent(t *testing.T, ctx context.Context, store *Store, name string) model.Agent {
	t.Helper()

	agent, err := store.CreateAgent(ctx, CreateAgentInput{
		ID:          newID(),
		Name:        name,
		Type:        model.AgentTypeAI,
		APIKeyHash:  newID(),
		Description: name,
		Status:      model.AgentStatusActive,
		Tags:        []string{"test"},
		Metadata:    map[string]any{},
	})
	if err != nil {
		t.Fatalf("create agent %s: %v", name, err)
	}
	return agent
}

func createDirectMessage(t *testing.T, ctx context.Context, store *Store, senderID, recipientID, content string) string {
	t.Helper()

	msgID := newID()
	to := recipientID
	_, err := store.CreateMessageWithRecipients(ctx, CreateMessageInput{
		ID:          msgID,
		FromAgentID: senderID,
		ToAgentID:   &to,
		ContentType: "text/plain",
		Content:     content,
		Priority:    model.MessagePriorityNormal,
		Metadata:    map[string]any{},
	}, []string{recipientID})
	if err != nil {
		t.Fatalf("create direct message: %v", err)
	}
	return msgID
}
