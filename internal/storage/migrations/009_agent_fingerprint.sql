-- Migration 009: add fingerprint for auto-registration identity
ALTER TABLE agents ADD COLUMN fingerprint TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_fingerprint ON agents(fingerprint) WHERE fingerprint IS NOT NULL;
