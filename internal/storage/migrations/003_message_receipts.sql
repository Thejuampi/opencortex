PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS message_receipts (
  id          TEXT PRIMARY KEY,
  message_id  TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  status      TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','read','expired')),
  delivered_at TEXT,
  read_at      TEXT,
  created_at   TEXT NOT NULL,
  UNIQUE(message_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_message_receipts_agent ON message_receipts(agent_id, status);
CREATE INDEX IF NOT EXISTS idx_message_receipts_message ON message_receipts(message_id);

