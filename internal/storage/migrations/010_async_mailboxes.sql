PRAGMA foreign_keys = OFF;

-- Per-agent cursor (high-water mark)
CREATE TABLE IF NOT EXISTS agent_cursors (
  agent_id        TEXT NOT NULL REFERENCES agents(id),
  channel         TEXT NOT NULL,
  cursor          TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  PRIMARY KEY (agent_id, channel)
);

ALTER TABLE message_receipts RENAME TO message_receipts_async_old;

CREATE TABLE message_receipts (
  id               TEXT PRIMARY KEY,
  message_id       TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id         TEXT REFERENCES agents(id) ON DELETE CASCADE,
  status           TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','read','expired','dead_letter')),
  delivered_at     TEXT,
  read_at          TEXT,
  created_at       TEXT NOT NULL,
  claim_token      TEXT,
  claim_expires_at TEXT,
  claim_attempts   INTEGER NOT NULL DEFAULT 0,
  last_claimed_at  TEXT,
  last_error       TEXT,
  attempt          INTEGER NOT NULL DEFAULT 0,
  max_attempts     INTEGER NOT NULL DEFAULT 3,
  ack_deadline_at  TEXT,
  UNIQUE(message_id, agent_id)
);

INSERT INTO message_receipts(
  id, message_id, agent_id, status, delivered_at, read_at, created_at,
  claim_token, claim_expires_at, claim_attempts, last_claimed_at, last_error, attempt, max_attempts, ack_deadline_at
)
SELECT
  id, message_id, agent_id, status, delivered_at, read_at, created_at,
  claim_token, claim_expires_at, claim_attempts, last_claimed_at, last_error, 0 as attempt, 3 as max_attempts, NULL as ack_deadline_at
FROM message_receipts_async_old;

DROP TABLE message_receipts_async_old;

CREATE INDEX IF NOT EXISTS idx_message_receipts_agent ON message_receipts(agent_id, status);
CREATE INDEX IF NOT EXISTS idx_message_receipts_message ON message_receipts(message_id);
CREATE INDEX IF NOT EXISTS idx_message_receipts_claim_lookup
  ON message_receipts(agent_id, status, claim_expires_at);
CREATE INDEX IF NOT EXISTS idx_message_receipts_claim_token
  ON message_receipts(message_id, agent_id, claim_token);

CREATE INDEX IF NOT EXISTS idx_deliveries_deadline
  ON message_receipts(ack_deadline_at)
  WHERE status = 'delivered';

PRAGMA foreign_keys = ON;
