PRAGMA foreign_keys = OFF;

CREATE TABLE IF NOT EXISTS groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT DEFAULT '',
  mode        TEXT NOT NULL DEFAULT 'fanout' CHECK(mode IN ('fanout','queue')),
  created_by  TEXT NOT NULL REFERENCES agents(id),
  metadata    TEXT DEFAULT '{}',
  created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS group_members (
  group_id    TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  role        TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('member','admin')),
  joined_at   TEXT NOT NULL,
  PRIMARY KEY (group_id, agent_id)
);

ALTER TABLE messages RENAME TO messages_old;

CREATE TABLE messages (
  id             TEXT PRIMARY KEY,
  from_agent_id  TEXT NOT NULL REFERENCES agents(id),
  to_agent_id    TEXT REFERENCES agents(id),
  topic_id       TEXT REFERENCES topics(id),
  to_group_id    TEXT REFERENCES groups(id),
  queue_mode     INTEGER NOT NULL DEFAULT 0,
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
  CHECK (to_agent_id IS NOT NULL OR topic_id IS NOT NULL OR to_group_id IS NOT NULL)
);

INSERT INTO messages(
  id, from_agent_id, to_agent_id, topic_id, to_group_id, queue_mode, reply_to_id, content_type, content,
  status, priority, tags, metadata, created_at, expires_at, delivered_at, read_at
)
SELECT
  id, from_agent_id, to_agent_id, topic_id, NULL AS to_group_id, 0 AS queue_mode, reply_to_id, content_type, content,
  status, priority, tags, metadata, created_at, expires_at, delivered_at, read_at
FROM messages_old;

ALTER TABLE message_receipts RENAME TO message_receipts_old;

CREATE TABLE message_receipts (
  id               TEXT PRIMARY KEY,
  message_id       TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id         TEXT REFERENCES agents(id) ON DELETE CASCADE,
  status           TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','read','expired')),
  delivered_at     TEXT,
  read_at          TEXT,
  created_at       TEXT NOT NULL,
  claim_token      TEXT,
  claim_expires_at TEXT,
  claim_attempts   INTEGER NOT NULL DEFAULT 0,
  last_claimed_at  TEXT,
  last_error       TEXT,
  UNIQUE(message_id, agent_id)
);

INSERT INTO message_receipts(
  id, message_id, agent_id, status, delivered_at, read_at, created_at,
  claim_token, claim_expires_at, claim_attempts, last_claimed_at, last_error
)
SELECT
  id, message_id, agent_id, status, delivered_at, read_at, created_at,
  claim_token, claim_expires_at, claim_attempts, last_claimed_at, last_error
FROM message_receipts_old;

DROP TABLE message_receipts_old;
DROP TABLE messages_old;

CREATE INDEX IF NOT EXISTS idx_messages_topic_id ON messages(topic_id);
CREATE INDEX IF NOT EXISTS idx_messages_to_agent_id ON messages(to_agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_to_group_id ON messages(to_group_id);
CREATE INDEX IF NOT EXISTS idx_messages_from_agent_id ON messages(from_agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);
CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(status);

CREATE INDEX IF NOT EXISTS idx_message_receipts_agent ON message_receipts(agent_id, status);
CREATE INDEX IF NOT EXISTS idx_message_receipts_message ON message_receipts(message_id);
CREATE INDEX IF NOT EXISTS idx_message_receipts_claim_lookup
  ON message_receipts(agent_id, status, claim_expires_at);
CREATE INDEX IF NOT EXISTS idx_message_receipts_claim_token
  ON message_receipts(message_id, agent_id, claim_token);

CREATE INDEX IF NOT EXISTS idx_group_members_agent ON group_members(agent_id);
CREATE INDEX IF NOT EXISTS idx_group_members_group ON group_members(group_id);

PRAGMA foreign_keys = ON;
