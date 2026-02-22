package repos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"opencortex/internal/model"
)

type CreateAgentInput struct {
	ID          string
	Name        string
	Type        model.AgentType
	APIKeyHash  string
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
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO agents (id, name, type, api_key_hash, description, tags, status, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID,
		in.Name,
		string(in.Type),
		in.APIKeyHash,
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
