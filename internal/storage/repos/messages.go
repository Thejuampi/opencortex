package repos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"opencortex/internal/model"
)

type CreateMessageInput struct {
	ID          string
	FromAgentID string
	ToAgentID   *string
	TopicID     *string
	ToGroupID   *string
	QueueMode   bool
	ReplyToID   *string
	ContentType string
	Content     string
	Status      model.MessageStatus
	Priority    model.MessagePriority
	Tags        []string
	Metadata    map[string]any
	ExpiresAt   *time.Time
}

type MessageFilters struct {
	Status      string
	TopicID     string
	FromAgentID string
	Priority    string
	Since       string
	Page        int
	PerPage     int
}

var ErrClaimNotFound = errors.New("claim_not_found")

type ClaimMessagesInput struct {
	AgentID      string
	Limit        int
	TopicID      string
	FromAgentID  string
	Priority     string
	LeaseSeconds int
}

type ClaimedMessage struct {
	Message        model.Message `json:"message"`
	ClaimToken     string        `json:"claim_token"`
	ClaimExpiresAt time.Time     `json:"claim_expires_at"`
	ClaimAttempts  int           `json:"claim_attempts"`
}

func (s *Store) CreateMessageWithRecipients(ctx context.Context, in CreateMessageInput, recipients []string) (model.Message, error) {
	if in.ContentType == "" {
		in.ContentType = "text/plain"
	}
	if in.Priority == "" {
		in.Priority = model.MessagePriorityNormal
	}
	if in.Status == "" {
		in.Status = model.MessageStatusPending
	}

	now := nowUTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Message{}, err
	}

	var expires sql.NullString
	if in.ExpiresAt != nil {
		expires.Valid = true
		expires.String = in.ExpiresAt.UTC().Format(timeFormat)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO messages(
  id, from_agent_id, to_agent_id, topic_id, to_group_id, queue_mode, reply_to_id, content_type, content, status, priority,
  tags, metadata, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID,
		in.FromAgentID,
		in.ToAgentID,
		in.TopicID,
		in.ToGroupID,
		boolToInt(in.QueueMode),
		in.ReplyToID,
		in.ContentType,
		in.Content,
		string(in.Status),
		string(in.Priority),
		toJSON(in.Tags),
		toJSON(in.Metadata),
		now.Format(timeFormat),
		expires,
	)
	if err != nil {
		_ = tx.Rollback()
		return model.Message{}, err
	}

	seen := map[string]struct{}{}
	for _, recipient := range recipients {
		if recipient == "" {
			continue
		}
		if _, ok := seen[recipient]; ok {
			continue
		}
		seen[recipient] = struct{}{}
		_, err := tx.ExecContext(ctx, `
INSERT INTO message_receipts(id, message_id, agent_id, status, created_at)
VALUES (?, ?, ?, 'pending', ?)`,
			newID(), in.ID, recipient, now.Format(timeFormat))
		if err != nil {
			_ = tx.Rollback()
			return model.Message{}, err
		}
	}
	if in.QueueMode && in.ToGroupID != nil {
		_, err := tx.ExecContext(ctx, `
INSERT INTO message_receipts(id, message_id, agent_id, status, created_at)
VALUES (?, ?, NULL, 'pending', ?)`,
			newID(), in.ID, now.Format(timeFormat))
		if err != nil {
			_ = tx.Rollback()
			return model.Message{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	return s.GetMessageByID(ctx, in.ID)
}

func (s *Store) GetMessageByID(ctx context.Context, id string) (model.Message, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, from_agent_id, to_agent_id, topic_id, to_group_id, queue_mode, reply_to_id, content_type, content,
       status, priority, tags, metadata, created_at, expires_at, delivered_at, read_at
FROM messages
WHERE id = ?`, id)
	return scanMessage(row)
}

func (s *Store) DeleteMessage(ctx context.Context, id, requesterAgentID string) error {
	var from string
	if err := s.DB.QueryRowContext(ctx, "SELECT from_agent_id FROM messages WHERE id = ?", id).Scan(&from); err != nil {
		return err
	}
	if from != requesterAgentID {
		return fmt.Errorf("sender_only")
	}
	_, err := s.DB.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", id)
	return err
}

func (s *Store) ListInbox(ctx context.Context, agentID string, f MessageFilters) ([]model.Message, int, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PerPage <= 0 {
		f.PerPage = 50
	}

	where := "WHERE mr.agent_id = ?"
	args := []any{agentID}
	if f.Status != "" {
		where += " AND mr.status = ?"
		args = append(args, f.Status)
	}
	if f.TopicID != "" {
		where += " AND m.topic_id = ?"
		args = append(args, f.TopicID)
	}
	if f.FromAgentID != "" {
		where += " AND m.from_agent_id = ?"
		args = append(args, f.FromAgentID)
	}
	if f.Priority != "" {
		where += " AND m.priority = ?"
		args = append(args, f.Priority)
	}
	if f.Since != "" {
		where += " AND m.created_at >= ?"
		args = append(args, f.Since)
	}

	var total int
	countQ := `
SELECT COUNT(*)
FROM message_receipts mr
JOIN messages m ON m.id = mr.message_id
` + where
	if err := s.DB.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
SELECT m.id, m.from_agent_id, m.to_agent_id, m.topic_id, m.to_group_id, m.queue_mode, m.reply_to_id, m.content_type, m.content,
       mr.status, m.priority, m.tags, m.metadata, m.created_at, m.expires_at, mr.delivered_at, mr.read_at
FROM message_receipts mr
JOIN messages m ON m.id = mr.message_id
` + where + `
ORDER BY m.created_at DESC
LIMIT ? OFFSET ?`
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []model.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, msg)
	}
	return out, total, rows.Err()
}

func (s *Store) ListMessagesByTopic(ctx context.Context, topicID string, page, perPage int) ([]model.Message, int, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 50
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE topic_id = ?", topicID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, from_agent_id, to_agent_id, topic_id, to_group_id, queue_mode, reply_to_id, content_type, content,
       status, priority, tags, metadata, created_at, expires_at, delivered_at, read_at
FROM messages
WHERE topic_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?`, topicID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, msg)
	}
	return out, total, rows.Err()
}

func (s *Store) ListAgentMessages(ctx context.Context, agentID string, page, perPage int) ([]model.Message, int, error) {
	return s.ListInbox(ctx, agentID, MessageFilters{Page: page, PerPage: perPage})
}

func (s *Store) MarkMessageRead(ctx context.Context, messageID, agentID string) error {
	now := nowUTC().Format(timeFormat)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE message_receipts
SET status = 'read', read_at = ?, delivered_at = COALESCE(delivered_at, ?)
WHERE message_id = ? AND agent_id = ?`, now, now, messageID, agentID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, _ = tx.ExecContext(ctx, `
UPDATE messages
SET status = CASE WHEN status = 'pending' THEN 'read' ELSE status END,
    read_at = COALESCE(read_at, ?),
    delivered_at = COALESCE(delivered_at, ?)
WHERE id = ?`, now, now, messageID)
	return tx.Commit()
}

func (s *Store) MarkMessageDelivered(ctx context.Context, messageID, agentID string) error {
	now := nowUTC().Format(timeFormat)
	_, err := s.DB.ExecContext(ctx, `
UPDATE message_receipts
SET status = CASE WHEN status = 'pending' THEN 'delivered' ELSE status END,
    delivered_at = COALESCE(delivered_at, ?)
WHERE message_id = ? AND agent_id = ?`, now, messageID, agentID)
	return err
}

func (s *Store) ClaimMessages(ctx context.Context, in ClaimMessagesInput) ([]ClaimedMessage, error) {
	if strings.TrimSpace(in.AgentID) == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if in.Limit <= 0 {
		in.Limit = 1
	}
	if in.LeaseSeconds <= 0 {
		in.LeaseSeconds = 300
	}

	now := nowUTC()
	nowTS := now.Format(timeFormat)
	leaseExpiry := now.Add(time.Duration(in.LeaseSeconds) * time.Second)
	leaseExpiryTS := leaseExpiry.Format(timeFormat)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	where := `WHERE mr.status = 'pending'
  AND (mr.claim_expires_at IS NULL OR mr.claim_expires_at <= ?)
  AND (m.expires_at IS NULL OR m.expires_at > ?)
  AND (
    mr.agent_id = ?
    OR (
      m.queue_mode = 1
      AND EXISTS (
        SELECT 1 FROM group_members gm
        WHERE gm.group_id = m.to_group_id AND gm.agent_id = ?
      )
    )
  )`
	args := []any{nowTS, nowTS, in.AgentID, in.AgentID}
	if in.TopicID != "" {
		where += " AND m.topic_id = ?"
		args = append(args, in.TopicID)
	}
	if in.FromAgentID != "" {
		where += " AND m.from_agent_id = ?"
		args = append(args, in.FromAgentID)
	}
	if in.Priority != "" {
		where += " AND m.priority = ?"
		args = append(args, in.Priority)
	}

	rows, err := tx.QueryContext(ctx, `
SELECT mr.message_id, mr.agent_id, m.queue_mode
FROM message_receipts mr
JOIN messages m ON m.id = mr.message_id
`+where+`
ORDER BY m.created_at ASC
LIMIT ?`, append(args, in.Limit)...)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	defer rows.Close()

	type claimCandidate struct {
		MessageID string
		AgentID   sql.NullString
		QueueMode int
	}
	var candidates []claimCandidate
	for rows.Next() {
		var c claimCandidate
		if err := rows.Scan(&c.MessageID, &c.AgentID, &c.QueueMode); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	out := make([]ClaimedMessage, 0, len(candidates))
	for _, candidate := range candidates {
		token := newID()
		var (
			res sql.Result
			err error
		)
		if candidate.QueueMode == 1 {
			res, err = tx.ExecContext(ctx, `
UPDATE message_receipts
SET agent_id = ?,
    claim_token = ?,
    claim_expires_at = ?,
    claim_attempts = claim_attempts + 1,
    last_claimed_at = ?,
    last_error = NULL
WHERE message_id = ?
  AND status = 'pending'
  AND (claim_expires_at IS NULL OR claim_expires_at <= ?)
  AND EXISTS (
    SELECT 1 FROM messages m
    JOIN group_members gm ON gm.group_id = m.to_group_id
    WHERE m.id = message_receipts.message_id
      AND m.queue_mode = 1
      AND gm.agent_id = ?
  )
`, in.AgentID, token, leaseExpiryTS, nowTS, candidate.MessageID, nowTS, in.AgentID)
		} else if candidate.AgentID.Valid {
			res, err = tx.ExecContext(ctx, `
UPDATE message_receipts
SET claim_token = ?,
    claim_expires_at = ?,
    claim_attempts = claim_attempts + 1,
    last_claimed_at = ?,
    last_error = NULL
WHERE message_id = ?
  AND agent_id = ?
  AND status = 'pending'
  AND (claim_expires_at IS NULL OR claim_expires_at <= ?)
`, token, leaseExpiryTS, nowTS, candidate.MessageID, in.AgentID, nowTS)
		} else {
			res, err = tx.ExecContext(ctx, `
UPDATE message_receipts
SET agent_id = ?,
    claim_token = ?,
    claim_expires_at = ?,
    claim_attempts = claim_attempts + 1,
    last_claimed_at = ?,
    last_error = NULL
WHERE message_id = ?
  AND agent_id IS NULL
  AND status = 'pending'
  AND (claim_expires_at IS NULL OR claim_expires_at <= ?)
  AND EXISTS (
    SELECT 1 FROM messages m
    JOIN group_members gm ON gm.group_id = m.to_group_id
    WHERE m.id = message_receipts.message_id
      AND m.queue_mode = 1
      AND gm.agent_id = ?
  )
`, in.AgentID, token, leaseExpiryTS, nowTS, candidate.MessageID, nowTS, in.AgentID)
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			continue
		}

		msg, err := s.getMessageForAgentTx(ctx, tx, candidate.MessageID, in.AgentID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}

		var attempts int
		if err := tx.QueryRowContext(ctx, `
SELECT claim_attempts
FROM message_receipts
WHERE message_id = ? AND agent_id = ?`, candidate.MessageID, in.AgentID).Scan(&attempts); err != nil {
			_ = tx.Rollback()
			return nil, err
		}

		out = append(out, ClaimedMessage{
			Message:        msg,
			ClaimToken:     token,
			ClaimExpiresAt: leaseExpiry,
			ClaimAttempts:  attempts,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AckMessageClaim(ctx context.Context, messageID, agentID, claimToken string, markRead bool) error {
	now := nowUTC().Format(timeFormat)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	status := "delivered"
	readAtArg := any(nil)
	if markRead {
		status = "read"
		readAtArg = now
	}

	res, err := tx.ExecContext(ctx, `
UPDATE message_receipts
SET status = ?,
    delivered_at = COALESCE(delivered_at, ?),
    read_at = COALESCE(read_at, ?),
    claim_token = NULL,
    claim_expires_at = NULL,
    last_error = NULL
WHERE message_id = ?
  AND agent_id = ?
  AND claim_token = ?
  AND claim_expires_at IS NOT NULL
  AND claim_expires_at > ?
  AND status = 'pending'`, status, now, readAtArg, messageID, agentID, claimToken, now)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		_ = tx.Rollback()
		return ErrClaimNotFound
	}

	if markRead {
		_, _ = tx.ExecContext(ctx, `
UPDATE messages
SET status = CASE WHEN status IN ('pending','delivered') THEN 'read' ELSE status END,
    read_at = COALESCE(read_at, ?),
    delivered_at = COALESCE(delivered_at, ?)
WHERE id = ?`, now, now, messageID)
	} else {
		_, _ = tx.ExecContext(ctx, `
UPDATE messages
SET status = CASE WHEN status = 'pending' THEN 'delivered' ELSE status END,
    delivered_at = COALESCE(delivered_at, ?)
WHERE id = ?`, now, messageID)
	}

	return tx.Commit()
}

func (s *Store) NackMessageClaim(ctx context.Context, messageID, agentID, claimToken, reason string) error {
	now := nowUTC().Format(timeFormat)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE message_receipts
SET claim_token = NULL,
    claim_expires_at = NULL,
    last_error = ?
WHERE message_id = ?
  AND agent_id = ?
  AND claim_token = ?
  AND claim_expires_at IS NOT NULL
  AND claim_expires_at > ?
  AND status = 'pending'`, reason, messageID, agentID, claimToken, now)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		_ = tx.Rollback()
		return ErrClaimNotFound
	}
	return tx.Commit()
}

func (s *Store) RenewMessageClaim(ctx context.Context, messageID, agentID, claimToken string, leaseSeconds int) (time.Time, error) {
	if leaseSeconds <= 0 {
		leaseSeconds = 300
	}
	now := nowUTC()
	nowTS := now.Format(timeFormat)
	expiresAt := now.Add(time.Duration(leaseSeconds) * time.Second)
	expiresTS := expiresAt.Format(timeFormat)
	res, err := s.DB.ExecContext(ctx, `
UPDATE message_receipts
SET claim_expires_at = ?
WHERE message_id = ?
  AND agent_id = ?
  AND claim_token = ?
  AND claim_expires_at IS NOT NULL
  AND claim_expires_at > ?
  AND status = 'pending'`, expiresTS, messageID, agentID, claimToken, nowTS)
	if err != nil {
		return time.Time{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return time.Time{}, ErrClaimNotFound
	}
	return expiresAt, nil
}

func (s *Store) getMessageForAgentTx(ctx context.Context, tx *sql.Tx, messageID, agentID string) (model.Message, error) {
	row := tx.QueryRowContext(ctx, `
SELECT m.id, m.from_agent_id, m.to_agent_id, m.topic_id, m.to_group_id, m.queue_mode, m.reply_to_id, m.content_type, m.content,
       mr.status, m.priority, m.tags, m.metadata, m.created_at, m.expires_at, mr.delivered_at, mr.read_at
FROM message_receipts mr
JOIN messages m ON m.id = mr.message_id
WHERE mr.message_id = ? AND mr.agent_id = ?`, messageID, agentID)
	return scanMessage(row)
}

func (s *Store) MessageThread(ctx context.Context, messageID string) ([]model.Message, error) {
	rows, err := s.DB.QueryContext(ctx, `
WITH RECURSIVE thread AS (
  SELECT id FROM messages WHERE id = ?
  UNION ALL
  SELECT m.id FROM messages m
  JOIN thread t ON m.reply_to_id = t.id
)
SELECT m.id, m.from_agent_id, m.to_agent_id, m.topic_id, m.to_group_id, m.queue_mode, m.reply_to_id, m.content_type, m.content,
       m.status, m.priority, m.tags, m.metadata, m.created_at, m.expires_at, m.delivered_at, m.read_at
FROM messages m
JOIN thread t ON t.id = m.id
ORDER BY m.created_at ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (s *Store) PurgeExpired(ctx context.Context) (int64, error) {
	now := nowUTC().Format(timeFormat)
	_, _ = s.DB.ExecContext(ctx, "UPDATE message_receipts SET status = 'expired' WHERE message_id IN (SELECT id FROM messages WHERE expires_at IS NOT NULL AND expires_at <= ?)", now)
	res, err := s.DB.ExecContext(ctx, "DELETE FROM messages WHERE expires_at IS NOT NULL AND expires_at <= ?", now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanMessage(scanner interface {
	Scan(dest ...any) error
}) (model.Message, error) {
	var (
		m         model.Message
		toAgentID sql.NullString
		topicID   sql.NullString
		toGroupID sql.NullString
		queueMode int
		replyToID sql.NullString
		status    string
		priority  string
		tags      string
		metadata  string
		createdAt string
		expiresAt sql.NullString
		delivered sql.NullString
		readAt    sql.NullString
	)
	if err := scanner.Scan(
		&m.ID,
		&m.FromAgentID,
		&toAgentID,
		&topicID,
		&toGroupID,
		&queueMode,
		&replyToID,
		&m.ContentType,
		&m.Content,
		&status,
		&priority,
		&tags,
		&metadata,
		&createdAt,
		&expiresAt,
		&delivered,
		&readAt,
	); err != nil {
		return model.Message{}, err
	}
	if toAgentID.Valid {
		m.ToAgentID = &toAgentID.String
	}
	if topicID.Valid {
		m.TopicID = &topicID.String
	}
	if toGroupID.Valid {
		m.ToGroupID = &toGroupID.String
	}
	m.QueueMode = queueMode == 1
	if replyToID.Valid {
		m.ReplyToID = &replyToID.String
	}
	m.Status = model.MessageStatus(status)
	m.Priority = model.MessagePriority(priority)
	m.Tags = fromJSON[[]string](tags)
	m.Metadata = fromJSON[map[string]any](metadata)
	m.CreatedAt = parseTS(createdAt)
	m.ExpiresAt = parseTSPtr(expiresAt)
	m.DeliveredAt = parseTSPtr(delivered)
	m.ReadAt = parseTSPtr(readAt)
	return m, nil
}

func (s *Store) RecipientsForTopic(ctx context.Context, topicID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT agent_id FROM subscriptions WHERE topic_id = ?", topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) TrimMessageVersions(ctx context.Context, max int) error {
	if max <= 0 {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM knowledge_versions
WHERE id IN (
  SELECT id FROM knowledge_versions kv
  WHERE kv.knowledge_id IN (
    SELECT knowledge_id FROM knowledge_versions GROUP BY knowledge_id HAVING COUNT(*) > ?
  )
  AND kv.version NOT IN (
    SELECT version FROM knowledge_versions kv2
    WHERE kv2.knowledge_id = kv.knowledge_id
    ORDER BY version DESC
    LIMIT ?
  )
)`, max, max)
	return err
}

func normalizeCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	items := strings.Split(v, ",")
	out := make([]string, 0, len(items))
	for _, it := range items {
		t := strings.TrimSpace(it)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
