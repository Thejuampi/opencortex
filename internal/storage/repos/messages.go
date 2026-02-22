package repos

import (
	"context"
	"database/sql"
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
  id, from_agent_id, to_agent_id, topic_id, reply_to_id, content_type, content, status, priority,
  tags, metadata, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID,
		in.FromAgentID,
		in.ToAgentID,
		in.TopicID,
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

	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	return s.GetMessageByID(ctx, in.ID)
}

func (s *Store) GetMessageByID(ctx context.Context, id string) (model.Message, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, from_agent_id, to_agent_id, topic_id, reply_to_id, content_type, content,
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
SELECT m.id, m.from_agent_id, m.to_agent_id, m.topic_id, m.reply_to_id, m.content_type, m.content,
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
SELECT id, from_agent_id, to_agent_id, topic_id, reply_to_id, content_type, content,
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

func (s *Store) MessageThread(ctx context.Context, messageID string) ([]model.Message, error) {
	rows, err := s.DB.QueryContext(ctx, `
WITH RECURSIVE thread AS (
  SELECT id FROM messages WHERE id = ?
  UNION ALL
  SELECT m.id FROM messages m
  JOIN thread t ON m.reply_to_id = t.id
)
SELECT m.id, m.from_agent_id, m.to_agent_id, m.topic_id, m.reply_to_id, m.content_type, m.content,
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
