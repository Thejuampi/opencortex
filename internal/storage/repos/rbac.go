package repos

import (
	"context"
	"database/sql"

	"opencortex/internal/auth"
	"opencortex/internal/model"
)

func (s *Store) SeedRBAC(ctx context.Context) error {
	roles := []struct {
		Name        string
		Description string
	}{
		{Name: "admin", Description: "Full access"},
		{Name: "agent", Description: "Read/write access for standard agent operations"},
		{Name: "readonly", Description: "Read-only access"},
		{Name: "sync", Description: "Sync-only credentials"},
	}
	for _, r := range roles {
		_, err := s.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO roles(id, name, description) VALUES (?, ?, ?)`, newID(), r.Name, r.Description)
		if err != nil {
			return err
		}
	}

	resourceActions := []struct {
		Resource string
		Action   string
	}{
		{"agents", "read"}, {"agents", "write"}, {"agents", "manage"},
		{"topics", "read"}, {"topics", "write"}, {"topics", "manage"},
		{"messages", "read"}, {"messages", "write"}, {"messages", "manage"},
		{"knowledge", "read"}, {"knowledge", "write"}, {"knowledge", "manage"},
		{"collections", "read"}, {"collections", "write"}, {"collections", "manage"},
		{"sync", "read"}, {"sync", "write"}, {"sync", "manage"},
		{"admin", "read"}, {"admin", "write"}, {"admin", "manage"},
	}
	for _, p := range resourceActions {
		_, err := s.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO permissions(id, resource, action, description) VALUES (?, ?, ?, ?)`,
			newID(), p.Resource, p.Action, p.Resource+" "+p.Action)
		if err != nil {
			return err
		}
	}

	for role, perms := range map[string][]auth.PermissionKey{
		"admin":    {{Resource: "*", Action: "*"}},
		"agent":    auth.DefaultPermissionsForRole("agent"),
		"readonly": auth.DefaultPermissionsForRole("readonly"),
		"sync":     auth.DefaultPermissionsForRole("sync"),
	} {
		roleID, err := s.roleID(ctx, role)
		if err != nil {
			return err
		}
		if role == "admin" {
			rows, err := s.DB.QueryContext(ctx, "SELECT id FROM permissions")
			if err != nil {
				return err
			}
			for rows.Next() {
				var permID string
				if err := rows.Scan(&permID); err != nil {
					_ = rows.Close()
					return err
				}
				_, err = s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO role_permissions(role_id, permission_id) VALUES (?, ?)", roleID, permID)
				if err != nil {
					_ = rows.Close()
					return err
				}
			}
			_ = rows.Close()
			continue
		}
		for _, key := range perms {
			permID, err := s.permissionID(ctx, key.Resource, key.Action)
			if err != nil {
				return err
			}
			_, err = s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO role_permissions(role_id, permission_id) VALUES (?, ?)", roleID, permID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) AssignRole(ctx context.Context, agentID, roleName string) error {
	roleID, err := s.roleID(ctx, roleName)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO agent_roles(agent_id, role_id, assigned_at)
VALUES (?, ?, ?)`, agentID, roleID, nowUTC().Format(timeFormat))
	return err
}

func (s *Store) RevokeRole(ctx context.Context, agentID, roleName string) error {
	roleID, err := s.roleID(ctx, roleName)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "DELETE FROM agent_roles WHERE agent_id = ? AND role_id = ?", agentID, roleID)
	return err
}

func (s *Store) AgentRoles(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT r.name
FROM agent_roles ar
JOIN roles r ON r.id = ar.role_id
WHERE ar.agent_id = ?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

func (s *Store) AgentPermissionSet(ctx context.Context, agentID string) (map[string]struct{}, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.resource, p.action
FROM agent_roles ar
JOIN role_permissions rp ON rp.role_id = ar.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE ar.agent_id = ?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]struct{}{}
	for rows.Next() {
		var resource, action string
		if err := rows.Scan(&resource, &action); err != nil {
			return nil, err
		}
		set[resource+":"+action] = struct{}{}
		set[resource+":*"] = struct{}{}
	}
	return set, rows.Err()
}

func (s *Store) ListRolesWithPermissions(ctx context.Context) ([]model.Role, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, description FROM roles ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Role
	for rows.Next() {
		var r model.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		perms, err := s.permissionsForRole(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		r.Permissions = perms
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) roleID(ctx context.Context, roleName string) (string, error) {
	var roleID string
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM roles WHERE name = ?", roleName).Scan(&roleID)
	return roleID, err
}

func (s *Store) permissionID(ctx context.Context, resource, action string) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM permissions WHERE resource = ? AND action = ?", resource, action).Scan(&id)
	return id, err
}

func (s *Store) permissionsForRole(ctx context.Context, roleID string) ([]model.Permission, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.id, p.resource, p.action, p.description
FROM role_permissions rp
JOIN permissions p ON p.id = rp.permission_id
WHERE rp.role_id = ?
ORDER BY p.resource, p.action`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Permission
	for rows.Next() {
		var p model.Permission
		if err := rows.Scan(&p.ID, &p.Resource, &p.Action, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) EnsureRoleAssignment(ctx context.Context, agentID string, defaultRole string) error {
	var c int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_roles WHERE agent_id = ?", agentID).Scan(&c)
	if err != nil {
		return err
	}
	if c > 0 {
		return nil
	}
	return s.AssignRole(ctx, agentID, defaultRole)
}

func (s *Store) IsAgentWithRole(ctx context.Context, agentID string, roleName string) (bool, error) {
	roleID, err := s.roleID(ctx, roleName)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	var exists int
	err = s.DB.QueryRowContext(ctx, "SELECT 1 FROM agent_roles WHERE agent_id = ? AND role_id = ? LIMIT 1", agentID, roleID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
