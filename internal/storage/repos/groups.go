package repos

import (
	"context"
	"database/sql"
	"strings"

	"opencortex/internal/model"
)

type CreateGroupInput struct {
	ID          string
	Name        string
	Description string
	Mode        model.GroupMode
	CreatedBy   string
	Metadata    map[string]any
}

func (s *Store) CreateGroup(ctx context.Context, in CreateGroupInput) (model.Group, error) {
	if in.Mode == "" {
		in.Mode = model.GroupModeFanout
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO groups(id, name, description, mode, created_by, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.Name, in.Description, string(in.Mode), in.CreatedBy, toJSON(in.Metadata), nowUTC().Format(timeFormat))
	if err != nil {
		return model.Group{}, err
	}
	return s.GetGroupByID(ctx, in.ID)
}

func (s *Store) ListGroups(ctx context.Context, page, perPage int, q string) ([]model.Group, int, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 50
	}

	where := "WHERE 1=1"
	args := []any{}
	if strings.TrimSpace(q) != "" {
		where += " AND (name LIKE ? OR description LIKE ?)"
		pat := "%" + q + "%"
		args = append(args, pat, pat)
	}

	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, description, mode, created_by, metadata, created_at
FROM groups `+where+`
ORDER BY created_at DESC
LIMIT ? OFFSET ?`, append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []model.Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, rows.Err()
}

func (s *Store) GetGroupByID(ctx context.Context, id string) (model.Group, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, name, description, mode, created_by, metadata, created_at
FROM groups
WHERE id = ?`, id)
	return scanGroup(row)
}

func (s *Store) UpdateGroup(ctx context.Context, id string, description *string, mode *string, metadata map[string]any) (model.Group, error) {
	set := []string{}
	args := []any{}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	if mode != nil {
		set = append(set, "mode = ?")
		args = append(args, *mode)
	}
	if metadata != nil {
		set = append(set, "metadata = ?")
		args = append(args, toJSON(metadata))
	}
	if len(set) == 0 {
		return s.GetGroupByID(ctx, id)
	}
	args = append(args, id)
	query := "UPDATE groups SET " + strings.Join(set, ", ") + " WHERE id = ?"
	if _, err := s.DB.ExecContext(ctx, query, args...); err != nil {
		return model.Group{}, err
	}
	return s.GetGroupByID(ctx, id)
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM groups WHERE id = ?", id)
	return err
}

func (s *Store) AddGroupMember(ctx context.Context, groupID, agentID, role string) error {
	if strings.TrimSpace(role) == "" {
		role = "member"
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT OR REPLACE INTO group_members(group_id, agent_id, role, joined_at)
VALUES (?, ?, ?, ?)
`, groupID, agentID, role, nowUTC().Format(timeFormat))
	return err
}

func (s *Store) RemoveGroupMember(ctx context.Context, groupID, agentID string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM group_members WHERE group_id = ? AND agent_id = ?", groupID, agentID)
	return err
}

func (s *Store) ListGroupMembers(ctx context.Context, groupID string) ([]model.GroupMember, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT gm.group_id, gm.agent_id, gm.role, gm.joined_at,
       a.id, a.name, a.type, a.description, a.tags, a.status, a.metadata, a.created_at, a.last_seen
FROM group_members gm
JOIN agents a ON a.id = gm.agent_id
WHERE gm.group_id = ?
ORDER BY gm.joined_at DESC
`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.GroupMember
	for rows.Next() {
		m, err := scanGroupMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ListGroupMemberIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT agent_id FROM group_members WHERE group_id = ?", groupID)
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

func scanGroup(scanner interface{ Scan(dest ...any) error }) (model.Group, error) {
	var (
		g         model.Group
		mode      string
		metadata  string
		createdAt string
	)
	if err := scanner.Scan(&g.ID, &g.Name, &g.Description, &mode, &g.CreatedBy, &metadata, &createdAt); err != nil {
		return model.Group{}, err
	}
	g.Mode = model.GroupMode(mode)
	g.Metadata = fromJSON[map[string]any](metadata)
	g.CreatedAt = parseTS(createdAt)
	return g, nil
}

func scanGroupMember(scanner interface{ Scan(dest ...any) error }) (model.GroupMember, error) {
	var (
		m        model.GroupMember
		joinedAt string
		agent    model.Agent
		typeRaw  string
		tags     string
		status   string
		metadata string
		created  string
		lastSeen sql.NullString
	)
	if err := scanner.Scan(
		&m.GroupID,
		&m.AgentID,
		&m.Role,
		&joinedAt,
		&agent.ID,
		&agent.Name,
		&typeRaw,
		&agent.Description,
		&tags,
		&status,
		&metadata,
		&created,
		&lastSeen,
	); err != nil {
		return model.GroupMember{}, err
	}
	m.JoinedAt = parseTS(joinedAt)
	agent.Type = model.AgentType(typeRaw)
	agent.Tags = fromJSON[[]string](tags)
	agent.Status = model.AgentStatus(status)
	agent.Metadata = fromJSON[map[string]any](metadata)
	agent.CreatedAt = parseTS(created)
	agent.LastSeen = parseTSPtr(lastSeen)
	m.Agent = agent
	return m, nil
}
