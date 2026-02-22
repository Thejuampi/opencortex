PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS sync_conflicts (
  id              TEXT PRIMARY KEY,
  manifest_id     TEXT NOT NULL REFERENCES sync_manifests(id) ON DELETE CASCADE,
  entity_type     TEXT NOT NULL,
  entity_id       TEXT NOT NULL,
  local_checksum  TEXT NOT NULL,
  remote_checksum TEXT NOT NULL,
  strategy        TEXT NOT NULL DEFAULT 'manual' CHECK(strategy IN ('local-wins','remote-wins','latest-wins','manual','fork')),
  status          TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','resolved')),
  local_payload   TEXT DEFAULT '{}',
  remote_payload  TEXT DEFAULT '{}',
  note            TEXT,
  created_at      TEXT NOT NULL,
  resolved_at     TEXT
);

CREATE INDEX IF NOT EXISTS idx_sync_conflicts_manifest ON sync_conflicts(manifest_id, status);

