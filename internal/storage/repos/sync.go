package repos

import (
	"context"
	"database/sql"
	"fmt"

	"opencortex/internal/model"
)

type CreateRemoteInput struct {
	ID         string
	RemoteURL  string
	RemoteName string
	Direction  model.SyncDirection
	Scope      model.SyncScope
	ScopeIDs   []string
	APIKeyHash string
	Strategy   string
	Schedule   *string
}

func (s *Store) CreateRemote(ctx context.Context, in CreateRemoteInput) (model.SyncManifest, error) {
	if in.Direction == "" {
		in.Direction = model.SyncDirectionBidirectional
	}
	if in.Scope == "" {
		in.Scope = model.SyncScopeFull
	}
	if in.Strategy == "" {
		in.Strategy = "latest-wins"
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO sync_manifests(id, remote_url, remote_name, direction, scope, scope_ids, api_key_hash, strategy, schedule, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.RemoteURL, in.RemoteName, string(in.Direction), string(in.Scope), toJSON(in.ScopeIDs), in.APIKeyHash, in.Strategy, in.Schedule, nowUTC().Format(timeFormat),
	)
	if err != nil {
		return model.SyncManifest{}, err
	}
	return s.GetRemote(ctx, in.RemoteName)
}

func (s *Store) ListRemotes(ctx context.Context) ([]model.SyncManifest, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, remote_url, remote_name, direction, scope, scope_ids, last_sync_at, last_sync_ok, created_at
FROM sync_manifests
ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SyncManifest
	for rows.Next() {
		r, err := scanSyncManifest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRemote(ctx context.Context, name string) (model.SyncManifest, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, remote_url, remote_name, direction, scope, scope_ids, last_sync_at, last_sync_ok, created_at
FROM sync_manifests
WHERE remote_name = ?`, name)
	return scanSyncManifest(row)
}

func (s *Store) GetRemoteWithAuth(ctx context.Context, name string) (model.SyncManifest, string, string, error) {
	var (
		m          model.SyncManifest
		direction  string
		scope      string
		scopeIDs   string
		lastSyncAt sql.NullString
		lastSyncOK sql.NullInt64
		createdAt  string
		apiKeyHash string
		strategy   string
	)
	err := s.DB.QueryRowContext(ctx, `
SELECT id, remote_url, remote_name, direction, scope, scope_ids, last_sync_at, last_sync_ok, created_at, api_key_hash, strategy
FROM sync_manifests
WHERE remote_name = ?`, name).Scan(
		&m.ID, &m.RemoteURL, &m.RemoteName, &direction, &scope, &scopeIDs,
		&lastSyncAt, &lastSyncOK, &createdAt, &apiKeyHash, &strategy,
	)
	if err != nil {
		return model.SyncManifest{}, "", "", err
	}
	m.Direction = model.SyncDirection(direction)
	m.Scope = model.SyncScope(scope)
	m.ScopeIDs = fromJSON[[]string](scopeIDs)
	if lastSyncAt.Valid {
		t := parseTS(lastSyncAt.String)
		if !t.IsZero() {
			m.LastSyncAt = &t
		}
	}
	if lastSyncOK.Valid {
		ok := lastSyncOK.Int64 == 1
		m.LastSyncOK = &ok
	}
	m.CreatedAt = parseTS(createdAt)
	return m, apiKeyHash, strategy, nil
}

func (s *Store) DeleteRemote(ctx context.Context, name string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sync_manifests WHERE remote_name = ?", name)
	return err
}

func (s *Store) CreateSyncLog(ctx context.Context, manifestID string, direction model.SyncDirection) (model.SyncLog, error) {
	id := newID()
	now := nowUTC().Format(timeFormat)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO sync_logs(id, manifest_id, direction, status, started_at)
VALUES (?, ?, ?, 'running', ?)`, id, manifestID, string(direction), now)
	if err != nil {
		return model.SyncLog{}, err
	}
	return s.GetSyncLog(ctx, id)
}

func (s *Store) CompleteSyncLog(ctx context.Context, id string, status model.SyncStatus, pushed, pulled, conflicts int, errMsg *string) error {
	finished := nowUTC().Format(timeFormat)
	_, err := s.DB.ExecContext(ctx, `
UPDATE sync_logs
SET status = ?, items_pushed = ?, items_pulled = ?, conflicts = ?, error_message = ?, finished_at = ?
WHERE id = ?`,
		string(status), pushed, pulled, conflicts, errMsg, finished, id)
	return err
}

func (s *Store) UpdateManifestSyncResult(ctx context.Context, manifestID string, ok bool) error {
	_, err := s.DB.ExecContext(ctx, `
UPDATE sync_manifests
SET last_sync_at = ?, last_sync_ok = ?
WHERE id = ?`, nowUTC().Format(timeFormat), boolToInt(ok), manifestID)
	return err
}

func (s *Store) GetSyncLog(ctx context.Context, id string) (model.SyncLog, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, manifest_id, direction, status, items_pushed, items_pulled, conflicts, error_message, started_at, finished_at
FROM sync_logs
WHERE id = ?`, id)
	return scanSyncLog(row)
}

func (s *Store) ListSyncLogs(ctx context.Context, limit int) ([]model.SyncLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, manifest_id, direction, status, items_pushed, items_pulled, conflicts, error_message, started_at, finished_at
FROM sync_logs
ORDER BY started_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SyncLog
	for rows.Next() {
		log, err := scanSyncLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, log)
	}
	return out, rows.Err()
}

func (s *Store) CreateSyncConflict(ctx context.Context, manifestID, entityType, entityID, localChecksum, remoteChecksum, strategy string, localPayload, remotePayload map[string]any) (model.SyncConflict, error) {
	id := newID()
	now := nowUTC().Format(timeFormat)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO sync_conflicts(
  id, manifest_id, entity_type, entity_id, local_checksum, remote_checksum,
  strategy, status, local_payload, remote_payload, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
		id, manifestID, entityType, entityID, localChecksum, remoteChecksum, strategy, toJSON(localPayload), toJSON(remotePayload), now,
	)
	if err != nil {
		return model.SyncConflict{}, err
	}
	return s.GetSyncConflict(ctx, id)
}

func (s *Store) GetSyncConflict(ctx context.Context, id string) (model.SyncConflict, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, manifest_id, entity_type, entity_id, local_checksum, remote_checksum, strategy, status, created_at, resolved_at
FROM sync_conflicts
WHERE id = ?`, id)
	return scanSyncConflict(row)
}

func (s *Store) ListOpenConflicts(ctx context.Context) ([]model.SyncConflict, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, manifest_id, entity_type, entity_id, local_checksum, remote_checksum, strategy, status, created_at, resolved_at
FROM sync_conflicts
WHERE status = 'open'
ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SyncConflict
	for rows.Next() {
		c, err := scanSyncConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ResolveConflict(ctx context.Context, id, strategy, note string) error {
	_, err := s.DB.ExecContext(ctx, `
UPDATE sync_conflicts
SET status = 'resolved', strategy = ?, note = ?, resolved_at = ?
WHERE id = ?`, strategy, note, nowUTC().Format(timeFormat), id)
	return err
}

func scanSyncManifest(scanner interface {
	Scan(dest ...any) error
}) (model.SyncManifest, error) {
	var (
		m          model.SyncManifest
		direction  string
		scope      string
		scopeIDs   string
		lastSyncAt sql.NullString
		lastSyncOK sql.NullInt64
		createdAt  string
	)
	if err := scanner.Scan(&m.ID, &m.RemoteURL, &m.RemoteName, &direction, &scope, &scopeIDs, &lastSyncAt, &lastSyncOK, &createdAt); err != nil {
		return model.SyncManifest{}, err
	}
	m.Direction = model.SyncDirection(direction)
	m.Scope = model.SyncScope(scope)
	m.ScopeIDs = fromJSON[[]string](scopeIDs)
	if lastSyncAt.Valid {
		t := parseTS(lastSyncAt.String)
		if !t.IsZero() {
			m.LastSyncAt = &t
		}
	}
	if lastSyncOK.Valid {
		v := lastSyncOK.Int64 == 1
		m.LastSyncOK = &v
	}
	m.CreatedAt = parseTS(createdAt)
	return m, nil
}

func scanSyncLog(scanner interface {
	Scan(dest ...any) error
}) (model.SyncLog, error) {
	var (
		l         model.SyncLog
		direction string
		status    string
		errMsg    sql.NullString
		started   string
		finished  sql.NullString
	)
	if err := scanner.Scan(&l.ID, &l.ManifestID, &direction, &status, &l.ItemsPushed, &l.ItemsPulled, &l.Conflicts, &errMsg, &started, &finished); err != nil {
		return model.SyncLog{}, err
	}
	l.Direction = model.SyncDirection(direction)
	l.Status = model.SyncStatus(status)
	if errMsg.Valid {
		l.ErrorMessage = &errMsg.String
	}
	l.StartedAt = parseTS(started)
	l.FinishedAt = parseTSPtr(finished)
	return l, nil
}

func scanSyncConflict(scanner interface {
	Scan(dest ...any) error
}) (model.SyncConflict, error) {
	var (
		c          model.SyncConflict
		resolvedAt sql.NullString
		createdAt  string
	)
	if err := scanner.Scan(&c.ID, &c.ManifestID, &c.EntityType, &c.EntityID, &c.LocalChecksum, &c.RemoteChecksum, &c.Strategy, &c.Status, &createdAt, &resolvedAt); err != nil {
		return model.SyncConflict{}, err
	}
	c.CreatedAt = parseTS(createdAt)
	c.ResolvedAt = parseTSPtr(resolvedAt)
	return c, nil
}

func (s *Store) ManifestByID(ctx context.Context, id string) (model.SyncManifest, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, remote_url, remote_name, direction, scope, scope_ids, last_sync_at, last_sync_ok, created_at
FROM sync_manifests WHERE id = ?`, id)
	return scanSyncManifest(row)
}

func (s *Store) AssertRemoteExists(ctx context.Context, name string) error {
	var exists int
	err := s.DB.QueryRowContext(ctx, "SELECT 1 FROM sync_manifests WHERE remote_name = ? LIMIT 1", name).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("not_found")
	}
	return err
}
