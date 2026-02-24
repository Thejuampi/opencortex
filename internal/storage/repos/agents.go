package repos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"opencortex/internal/model"
)

type CreateAgentInput struct {
	ID          string
	Name        string
	Type        model.AgentType
	APIKeyHash  string
	Fingerprint string
	Description string
	Tags        []string
	Status      model.AgentStatus
	Metadata    map[string]any
}

func (s *Store) CreateAgent(ctx context.Context, in CreateAgentInput) (model.Agent, error) {
	now := nowUTC()
	if in.Status == "" {
		in.Status = model.AgentStatusActive
	}
	fingerprintVal := sql.NullString{}
	if in.Fingerprint != "" {
		fingerprintVal = sql.NullString{String: in.Fingerprint, Valid: true}
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO agents (id, name, type, api_key_hash, fingerprint, description, tags, status, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID,
		in.Name,
		string(in.Type),
		in.APIKeyHash,
		fingerprintVal,
		in.Description,
		toJSON(in.Tags),
		string(in.Status),
		toJSON(in.Metadata),
		now.Format(timeFormat),
	)
	if err != nil {
		return model.Agent{}, fmt.Errorf("insert agent: %w", err)
	}
	return s.GetAgentByID(ctx, in.ID)
}

func (s *Store) ListAgents(ctx context.Context, status string, q string, page, perPage int) ([]model.Agent, int, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 50
	}

	where := "WHERE 1=1"
	args := []any{}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	if q != "" {
		where += " AND (name LIKE ? OR description LIKE ?)"
		pattern := "%" + q + "%"
		args = append(args, pattern, pattern)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `
SELECT id, name, type, description, tags, status, metadata, created_at, last_seen
FROM agents ` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, perPage, (page-1)*perPage)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func (s *Store) GetAgentByID(ctx context.Context, id string) (model.Agent, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, name, type, description, tags, status, metadata, created_at, last_seen
FROM agents WHERE id = ?`, id)
	return scanAgent(row)
}

func (s *Store) GetAgentByAPIKeyHash(ctx context.Context, hash string) (model.Agent, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, name, type, description, tags, status, metadata, created_at, last_seen
FROM agents WHERE api_key_hash = ?`, hash)
	return scanAgent(row)
}

func (s *Store) GetAgentByFingerprint(ctx context.Context, fingerprint string) (model.Agent, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, name, type, description, tags, status, metadata, created_at, last_seen
FROM agents WHERE fingerprint = ?`, fingerprint)
	return scanAgent(row)
}

func (s *Store) GetAgentHashByID(ctx context.Context, id string) (string, error) {
	var hash string
	if err := s.DB.QueryRowContext(ctx, "SELECT api_key_hash FROM agents WHERE id = ?", id).Scan(&hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (s *Store) UpdateAgent(ctx context.Context, id string, description *string, tags []string, metadata map[string]any, status *string) (model.Agent, error) {
	set := []string{}
	args := []any{}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	if tags != nil {
		set = append(set, "tags = ?")
		args = append(args, toJSON(tags))
	}
	if metadata != nil {
		set = append(set, "metadata = ?")
		args = append(args, toJSON(metadata))
	}
	if status != nil {
		set = append(set, "status = ?")
		args = append(args, *status)
	}
	if len(set) == 0 {
		return s.GetAgentByID(ctx, id)
	}
	args = append(args, id)
	query := "UPDATE agents SET " + strings.Join(set, ", ") + " WHERE id = ?"
	if _, err := s.DB.ExecContext(ctx, query, args...); err != nil {
		return model.Agent{}, err
	}
	return s.GetAgentByID(ctx, id)
}

func (s *Store) DeactivateAgent(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE agents SET status = 'inactive' WHERE id = ?", id)
	return err
}

func (s *Store) RotateAgentKey(ctx context.Context, id, newHash string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE agents SET api_key_hash = ? WHERE id = ?", newHash, id)
	return err
}

func (s *Store) UpdateLastSeen(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE agents SET last_seen = ? WHERE id = ?", nowUTC().Format(timeFormat), id)
	return err
}

func (s *Store) DeleteInactiveAutoAgentsCascade(ctx context.Context, cutoff time.Time) (int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT a.id
FROM agents a
WHERE a.status = 'active'
  AND a.tags LIKE '%"auto"%'
  AND COALESCE(a.last_seen, a.created_at) <= ?
  AND NOT EXISTS (
    SELECT 1
    FROM agent_roles ar
    JOIN roles r ON r.id = ar.role_id
    WHERE ar.agent_id = a.id AND r.name = 'admin'
  )`, cutoff.UTC().Format(timeFormat))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := deleteAgentCascadeTx(ctx, tx, id); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

func deleteAgentCascadeTx(ctx context.Context, tx *sql.Tx, agentID string) error {
	topicIDs, err := selectStringColumnTx(ctx, tx, "SELECT id FROM topics WHERE created_by = ?", agentID)
	if err != nil {
		return err
	}
	groupIDs, err := selectStringColumnTx(ctx, tx, "SELECT id FROM groups WHERE created_by = ?", agentID)
	if err != nil {
		return err
	}
	collectionIDs, err := selectStringColumnTx(ctx, tx, "SELECT id FROM collections WHERE created_by = ?", agentID)
	if err != nil {
		return err
	}

	msgIDs, err := selectMessageIDsForAgentCascade(ctx, tx, agentID, topicIDs, groupIDs)
	if err != nil {
		return err
	}
	if len(msgIDs) > 0 {
		if _, err := execInTx(ctx, tx, "UPDATE messages SET reply_to_id = NULL WHERE reply_to_id IN (%s)", msgIDs); err != nil {
			return err
		}
		if _, err := execInTx(ctx, tx, "DELETE FROM messages WHERE id IN (%s)", msgIDs); err != nil {
			return err
		}
	}

	// Preserve surviving knowledge while removing hard references to deleted agent.
	if _, err := tx.ExecContext(ctx, "UPDATE knowledge_entries SET updated_by = created_by WHERE updated_by = ?", agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE knowledge_versions
SET changed_by = (
  SELECT ke.created_by FROM knowledge_entries ke WHERE ke.id = knowledge_versions.knowledge_id
)
WHERE changed_by = ?`, agentID); err != nil {
		return err
	}

	if len(collectionIDs) > 0 {
		if _, err := execInTx(ctx, tx, "DELETE FROM knowledge_entries WHERE collection_id IN (%s)", collectionIDs); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_entries WHERE created_by = ?", agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_versions WHERE changed_by = ?", agentID); err != nil {
		return err
	}

	if len(topicIDs) > 0 {
		if _, err := execInTx(ctx, tx, "DELETE FROM topics WHERE id IN (%s)", topicIDs); err != nil {
			return err
		}
	}
	if len(groupIDs) > 0 {
		if _, err := execInTx(ctx, tx, "DELETE FROM groups WHERE id IN (%s)", groupIDs); err != nil {
			return err
		}
	}
	if len(collectionIDs) > 0 {
		if _, err := execInTx(ctx, tx, "DELETE FROM collections WHERE id IN (%s)", collectionIDs); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM topic_members WHERE added_by = ?", agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_logs WHERE agent_id = ?", agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM agent_cursors WHERE agent_id = ?", agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", agentID); err != nil {
		return err
	}
	return nil
}

func selectMessageIDsForAgentCascade(ctx context.Context, tx *sql.Tx, agentID string, topicIDs, groupIDs []string) ([]string, error) {
	query := "SELECT id FROM messages WHERE from_agent_id = ? OR to_agent_id = ?"
	args := []any{agentID, agentID}
	if len(topicIDs) > 0 {
		in, inArgs := inClause(topicIDs)
		query += " OR topic_id IN (" + in + ")"
		args = append(args, inArgs...)
	}
	if len(groupIDs) > 0 {
		in, inArgs := inClause(groupIDs)
		query += " OR to_group_id IN (" + in + ")"
		args = append(args, inArgs...)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func selectStringColumnTx(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func execInTx(ctx context.Context, tx *sql.Tx, queryFmt string, ids []string) (sql.Result, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	in, args := inClause(ids)
	return tx.ExecContext(ctx, fmt.Sprintf(queryFmt, in), args...)
}

func inClause(ids []string) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func scanAgent(scanner interface {
	Scan(dest ...any) error
}) (model.Agent, error) {
	var (
		a        model.Agent
		t        string
		tags     string
		status   string
		metadata string
		created  string
		lastSeen sql.NullString
	)
	if err := scanner.Scan(&a.ID, &a.Name, &t, &a.Description, &tags, &status, &metadata, &created, &lastSeen); err != nil {
		return model.Agent{}, err
	}
	a.Type = model.AgentType(t)
	a.Tags = fromJSON[[]string](tags)
	a.Status = model.AgentStatus(status)
	a.Metadata = fromJSON[map[string]any](metadata)
	a.CreatedAt = parseTS(created)
	if ls := parseTSPtr(lastSeen); ls != nil {
		a.LastSeen = ls
	}
	return a, nil
}
