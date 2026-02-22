-- Migration 011: enforce unique agent names (case-insensitive)
-- Deduplicate existing rows first so index creation succeeds on upgraded DBs.
WITH ranked AS (
  SELECT id, name,
         ROW_NUMBER() OVER (PARTITION BY lower(name) ORDER BY created_at, id) AS rn
  FROM agents
)
UPDATE agents
SET name = name || '-' || substr(id, 1, 8)
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_name_ci ON agents(lower(name));
