PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS roles (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS permissions (
  id          TEXT PRIMARY KEY,
  resource    TEXT NOT NULL,
  action      TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  UNIQUE(resource, action)
);

CREATE TABLE IF NOT EXISTS role_permissions (
  role_id       TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_id TEXT NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
  PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE IF NOT EXISTS agent_roles (
  agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  role_id     TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  assigned_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, role_id)
);

CREATE TABLE IF NOT EXISTS topic_members (
  topic_id    TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  added_by    TEXT NOT NULL REFERENCES agents(id),
  created_at  TEXT NOT NULL,
  PRIMARY KEY (topic_id, agent_id)
);

