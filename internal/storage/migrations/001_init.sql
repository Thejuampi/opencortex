PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS agents (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  type         TEXT NOT NULL CHECK(type IN ('human','ai','system')),
  api_key_hash TEXT NOT NULL UNIQUE,
  description  TEXT DEFAULT '',
  tags         TEXT DEFAULT '[]',
  status       TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','inactive','banned')),
  metadata     TEXT DEFAULT '{}',
  created_at   TEXT NOT NULL,
  last_seen    TEXT
);

CREATE TABLE IF NOT EXISTS topics (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT DEFAULT '',
  retention   TEXT NOT NULL DEFAULT 'persistent' CHECK(retention IN ('none','persistent','ttl')),
  ttl_seconds INTEGER,
  created_by  TEXT NOT NULL REFERENCES agents(id),
  is_public   INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS subscriptions (
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  topic_id   TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  filter     TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, topic_id)
);

CREATE TABLE IF NOT EXISTS messages (
  id             TEXT PRIMARY KEY,
  from_agent_id  TEXT NOT NULL REFERENCES agents(id),
  to_agent_id    TEXT REFERENCES agents(id),
  topic_id       TEXT REFERENCES topics(id),
  reply_to_id    TEXT REFERENCES messages(id),
  content_type   TEXT NOT NULL DEFAULT 'text/plain',
  content        TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','read','expired')),
  priority       TEXT NOT NULL DEFAULT 'normal' CHECK(priority IN ('low','normal','high','critical')),
  tags           TEXT DEFAULT '[]',
  metadata       TEXT DEFAULT '{}',
  created_at     TEXT NOT NULL,
  expires_at     TEXT,
  delivered_at   TEXT,
  read_at        TEXT,
  CHECK (to_agent_id IS NOT NULL OR topic_id IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS collections (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT DEFAULT '',
  parent_id   TEXT REFERENCES collections(id),
  created_by  TEXT NOT NULL REFERENCES agents(id),
  is_public   INTEGER NOT NULL DEFAULT 1,
  metadata    TEXT DEFAULT '{}',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_entries (
  id            TEXT PRIMARY KEY,
  title         TEXT NOT NULL,
  content       TEXT NOT NULL,
  content_type  TEXT NOT NULL DEFAULT 'text/markdown',
  summary       TEXT DEFAULT '',
  tags          TEXT DEFAULT '[]',
  collection_id TEXT REFERENCES collections(id),
  created_by    TEXT NOT NULL REFERENCES agents(id),
  updated_by    TEXT NOT NULL REFERENCES agents(id),
  version       INTEGER NOT NULL DEFAULT 1,
  checksum      TEXT NOT NULL,
  is_pinned     INTEGER NOT NULL DEFAULT 0,
  visibility    TEXT NOT NULL DEFAULT 'public' CHECK(visibility IN ('public','restricted')),
  source        TEXT,
  metadata      TEXT DEFAULT '{}',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(
  title,
  content,
  summary,
  tags,
  content=knowledge_entries,
  content_rowid=rowid
);

CREATE TABLE IF NOT EXISTS knowledge_versions (
  id           TEXT PRIMARY KEY,
  knowledge_id TEXT NOT NULL REFERENCES knowledge_entries(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,
  content      TEXT NOT NULL,
  summary      TEXT,
  changed_by   TEXT NOT NULL REFERENCES agents(id),
  change_note  TEXT,
  created_at   TEXT NOT NULL,
  UNIQUE(knowledge_id, version)
);

CREATE TABLE IF NOT EXISTS sync_manifests (
  id           TEXT PRIMARY KEY,
  remote_url   TEXT NOT NULL,
  remote_name  TEXT NOT NULL UNIQUE,
  direction    TEXT NOT NULL DEFAULT 'bidirectional' CHECK(direction IN ('push','pull','bidirectional')),
  scope        TEXT NOT NULL DEFAULT 'full' CHECK(scope IN ('full','collections','topics','messages')),
  scope_ids    TEXT DEFAULT '[]',
  api_key_hash TEXT NOT NULL DEFAULT '',
  strategy     TEXT NOT NULL DEFAULT 'latest-wins',
  schedule     TEXT,
  last_sync_at TEXT,
  last_sync_ok INTEGER,
  created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_logs (
  id            TEXT PRIMARY KEY,
  manifest_id   TEXT NOT NULL REFERENCES sync_manifests(id),
  direction     TEXT NOT NULL CHECK(direction IN ('push','pull')),
  status        TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','success','partial','failed')),
  items_pushed  INTEGER DEFAULT 0,
  items_pulled  INTEGER DEFAULT 0,
  conflicts     INTEGER DEFAULT 0,
  error_message TEXT,
  started_at    TEXT NOT NULL,
  finished_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_messages_topic_id ON messages(topic_id);
CREATE INDEX IF NOT EXISTS idx_messages_to_agent_id ON messages(to_agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_from_agent_id ON messages(from_agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);
CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(status);
CREATE INDEX IF NOT EXISTS idx_knowledge_collection ON knowledge_entries(collection_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_created_by ON knowledge_entries(created_by);
CREATE INDEX IF NOT EXISTS idx_knowledge_updated_at ON knowledge_entries(updated_at);
CREATE INDEX IF NOT EXISTS idx_knowledge_versions ON knowledge_versions(knowledge_id, version);

