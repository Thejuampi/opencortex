PRAGMA foreign_keys = ON;

ALTER TABLE message_receipts ADD COLUMN claim_token TEXT;
ALTER TABLE message_receipts ADD COLUMN claim_expires_at TEXT;
ALTER TABLE message_receipts ADD COLUMN claim_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE message_receipts ADD COLUMN last_claimed_at TEXT;
ALTER TABLE message_receipts ADD COLUMN last_error TEXT;

CREATE INDEX IF NOT EXISTS idx_message_receipts_claim_lookup
  ON message_receipts(agent_id, status, claim_expires_at);

CREATE INDEX IF NOT EXISTS idx_message_receipts_claim_token
  ON message_receipts(message_id, agent_id, claim_token);
