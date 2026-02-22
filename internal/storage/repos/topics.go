package repos

import (
	"context"
	"database/sql"
	"strings"

	"opencortex/internal/model"
)

type CreateTopicInput struct {
	ID          string
	Name        string
	Description string
	Retention   model.TopicRetention
	TTLSeconds  *int
	CreatedBy   string
	IsPublic    bool
}

func (s *Store) CreateTopic(ctx context.Context, in CreateTopicInput) (model.Topic, error) {
	if in.Retention == "" {
		in.Retention = model.TopicRetentionPersistent
	}
	now := nowUTC().Format(timeFormat)
	ttl := sql.NullInt64{}
	if in.TTLSeconds != nil {
		ttl.Valid = true
		ttl.Int64 = int64(*in.TTLSeconds)
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO topics(id, name, description, retention, ttl_seconds, created_by, is_public, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, in.Description, string(in.Retention), ttl, in.CreatedBy, boolToInt(in.IsPublic), now,
	)
	if err != nil {
		return model.Topic{}, err
	}
	return s.GetTopicByID(ctx, in.ID)
}

func (s *Store) ListTopics(ctx context.Context, page, perPage int, q string) ([]model.Topic, int, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 50
	}
	where := "WHERE 1=1"
	args := []any{}
	if q != "" {
		where += " AND (name LIKE ? OR description LIKE ?)"
		pattern := "%" + q + "%"
		args = append(args, pattern, pattern)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM topics "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `
SELECT id, name, description, retention, ttl_seconds, created_by, is_public, created_at
FROM topics ` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, perPage, (page-1)*perPage)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Topic
	for rows.Next() {
		t, err := scanTopic(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func (s *Store) GetTopicByID(ctx context.Context, id string) (model.Topic, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, name, description, retention, ttl_seconds, created_by, is_public, created_at
FROM topics WHERE id = ?`, id)
	return scanTopic(row)
}

func (s *Store) UpdateTopic(ctx context.Context, id string, description *string, retention *string, ttlSeconds *int, isPublic *bool) (model.Topic, error) {
	set := []string{}
	args := []any{}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	if retention != nil {
		set = append(set, "retention = ?")
		args = append(args, *retention)
	}
	if ttlSeconds != nil {
		set = append(set, "ttl_seconds = ?")
		args = append(args, *ttlSeconds)
	}
	if isPublic != nil {
		set = append(set, "is_public = ?")
		args = append(args, boolToInt(*isPublic))
	}
	if len(set) == 0 {
		return s.GetTopicByID(ctx, id)
	}
	args = append(args, id)
	query := "UPDATE topics SET " + strings.Join(set, ", ") + " WHERE id = ?"
	if _, err := s.DB.ExecContext(ctx, query, args...); err != nil {
		return model.Topic{}, err
	}
	return s.GetTopicByID(ctx, id)
}

func (s *Store) DeleteTopic(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM topics WHERE id = ?", id)
	return err
}

func (s *Store) Subscribe(ctx context.Context, agentID, topicID string, filter map[string]any) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO subscriptions(agent_id, topic_id, filter, created_at)
VALUES (?, ?, ?, ?)`, agentID, topicID, toJSON(filter), nowUTC().Format(timeFormat))
	return err
}

func (s *Store) Unsubscribe(ctx context.Context, agentID, topicID string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM subscriptions WHERE agent_id = ? AND topic_id = ?", agentID, topicID)
	return err
}

func (s *Store) ListTopicSubscribers(ctx context.Context, topicID string) ([]model.Agent, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT a.id, a.name, a.type, a.description, a.tags, a.status, a.metadata, a.created_at, a.last_seen
FROM subscriptions s
JOIN agents a ON a.id = s.agent_id
WHERE s.topic_id = ?
ORDER BY s.created_at DESC`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ListAgentSubscriptions(ctx context.Context, agentID string) ([]model.Topic, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT t.id, t.name, t.description, t.retention, t.ttl_seconds, t.created_by, t.is_public, t.created_at
FROM subscriptions s
JOIN topics t ON t.id = s.topic_id
WHERE s.agent_id = ?
ORDER BY s.created_at DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Topic
	for rows.Next() {
		t, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) AddTopicMember(ctx context.Context, topicID, agentID, addedBy string) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT OR REPLACE INTO topic_members(topic_id, agent_id, added_by, created_at)
VALUES (?, ?, ?, ?)`,
		topicID, agentID, addedBy, nowUTC().Format(timeFormat))
	return err
}

func (s *Store) RemoveTopicMember(ctx context.Context, topicID, agentID string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM topic_members WHERE topic_id = ? AND agent_id = ?", topicID, agentID)
	return err
}

func (s *Store) ListTopicMembers(ctx context.Context, topicID string) ([]model.Agent, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT a.id, a.name, a.type, a.description, a.tags, a.status, a.metadata, a.created_at, a.last_seen
FROM topic_members tm
JOIN agents a ON a.id = tm.agent_id
WHERE tm.topic_id = ?
ORDER BY tm.created_at DESC`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CanAccessTopic(ctx context.Context, topicID, agentID string) (bool, error) {
	var isPublic int
	if err := s.DB.QueryRowContext(ctx, "SELECT is_public FROM topics WHERE id = ?", topicID).Scan(&isPublic); err != nil {
		return false, err
	}
	if isPublic == 1 {
		return true, nil
	}
	var exists int
	err := s.DB.QueryRowContext(ctx, `
SELECT 1 FROM topic_members WHERE topic_id = ? AND agent_id = ? LIMIT 1`, topicID, agentID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func scanTopic(scanner interface {
	Scan(dest ...any) error
}) (model.Topic, error) {
	var (
		t         model.Topic
		retention string
		ttl       sql.NullInt64
		isPublic  int
		created   string
	)
	if err := scanner.Scan(&t.ID, &t.Name, &t.Description, &retention, &ttl, &t.CreatedBy, &isPublic, &created); err != nil {
		return model.Topic{}, err
	}
	t.Retention = model.TopicRetention(retention)
	if ttl.Valid {
		v := int(ttl.Int64)
		t.TTLSeconds = &v
	}
	t.IsPublic = isPublic == 1
	t.CreatedAt = parseTS(created)
	return t, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
