package repos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"

	"opencortex/internal/model"
)

type CreateKnowledgeInput struct {
	ID           string
	Title        string
	Content      string
	ContentType  string
	Summary      *string
	Tags         []string
	CollectionID *string
	CreatedBy    string
	Source       *string
	Metadata     map[string]any
	Visibility   model.KnowledgeVisibility
	ChangeNote   *string
}

type UpdateKnowledgeContentInput struct {
	ID          string
	Content     string
	Summary     *string
	UpdatedBy   string
	ChangeNote  *string
	ContentType string
}

type KnowledgeFilters struct {
	Query        string
	Tags         []string
	CollectionID string
	CreatedBy    string
	Since        string
	Pinned       *bool
	Page         int
	PerPage      int
}

func checksum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateKnowledge(ctx context.Context, in CreateKnowledgeInput) (model.KnowledgeEntry, error) {
	if in.ContentType == "" {
		in.ContentType = "text/markdown"
	}
	if in.Visibility == "" {
		in.Visibility = model.KnowledgeVisibilityPublic
	}
	now := nowUTC().Format(timeFormat)
	entryChecksum := checksum(in.Content)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.KnowledgeEntry{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO knowledge_entries(
  id, title, content, content_type, summary, tags, collection_id, created_by, updated_by, version,
  checksum, is_pinned, visibility, source, metadata, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 0, ?, ?, ?, ?, ?)`,
		in.ID,
		in.Title,
		in.Content,
		in.ContentType,
		nullStringFromPtr(in.Summary),
		toJSON(in.Tags),
		in.CollectionID,
		in.CreatedBy,
		in.CreatedBy,
		entryChecksum,
		string(in.Visibility),
		in.Source,
		toJSON(in.Metadata),
		now,
		now,
	)
	if err != nil {
		_ = tx.Rollback()
		return model.KnowledgeEntry{}, err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO knowledge_versions(id, knowledge_id, version, content, summary, changed_by, change_note, created_at)
VALUES (?, ?, 1, ?, ?, ?, ?, ?)`,
		newID(), in.ID, in.Content, nullStringFromPtr(in.Summary), in.CreatedBy, in.ChangeNote, now)
	if err != nil {
		_ = tx.Rollback()
		return model.KnowledgeEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.KnowledgeEntry{}, err
	}
	return s.GetKnowledge(ctx, in.ID)
}

func (s *Store) GetKnowledge(ctx context.Context, id string) (model.KnowledgeEntry, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, title, content, content_type, summary, tags, collection_id, created_by, updated_by, version,
       checksum, is_pinned, visibility, source, metadata, created_at, updated_at
FROM knowledge_entries
WHERE id = ?`, id)
	return scanKnowledge(row)
}

func (s *Store) SearchKnowledge(ctx context.Context, f KnowledgeFilters) ([]model.KnowledgeEntry, int, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PerPage <= 0 {
		f.PerPage = 20
	}
	where := "WHERE 1=1"
	args := []any{}
	if f.Query != "" {
		where += " AND ke.rowid IN (SELECT rowid FROM knowledge_fts WHERE knowledge_fts MATCH ?)"
		args = append(args, ftsLiteralQuery(f.Query))
	}
	if f.CollectionID != "" {
		where += " AND ke.collection_id = ?"
		args = append(args, f.CollectionID)
	}
	if f.CreatedBy != "" {
		where += " AND ke.created_by = ?"
		args = append(args, f.CreatedBy)
	}
	if f.Since != "" {
		where += " AND ke.updated_at >= ?"
		args = append(args, f.Since)
	}
	if f.Pinned != nil {
		where += " AND ke.is_pinned = ?"
		args = append(args, boolToInt(*f.Pinned))
	}
	for _, tag := range f.Tags {
		where += " AND ke.tags LIKE ?"
		args = append(args, "%\""+tag+"\"%")
	}
	var total int
	countQ := "SELECT COUNT(*) FROM knowledge_entries ke " + where
	if err := s.DB.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `
SELECT ke.id, ke.title, ke.content, ke.content_type, ke.summary, ke.tags, ke.collection_id, ke.created_by,
       ke.updated_by, ke.version, ke.checksum, ke.is_pinned, ke.visibility, ke.source, ke.metadata, ke.created_at,
       ke.updated_at
FROM knowledge_entries ke ` + where + `
ORDER BY ke.is_pinned DESC, ke.updated_at DESC
LIMIT ? OFFSET ?`
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.KnowledgeEntry
	for rows.Next() {
		e, err := scanKnowledge(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func ftsLiteralQuery(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, `"`+strings.ReplaceAll(part, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}

func (s *Store) UpdateKnowledgeContent(ctx context.Context, in UpdateKnowledgeContentInput) (model.KnowledgeEntry, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.KnowledgeEntry{}, err
	}
	var currentVersion int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM knowledge_entries WHERE id = ?", in.ID).Scan(&currentVersion); err != nil {
		_ = tx.Rollback()
		return model.KnowledgeEntry{}, err
	}
	nextVersion := currentVersion + 1
	_, err = tx.ExecContext(ctx, `
UPDATE knowledge_entries
SET content = ?, content_type = COALESCE(?, content_type), summary = ?, version = ?, checksum = ?,
    updated_by = ?, updated_at = ?
WHERE id = ?`,
		in.Content,
		nullString(in.ContentType),
		nullStringFromPtr(in.Summary),
		nextVersion,
		checksum(in.Content),
		in.UpdatedBy,
		nowUTC().Format(timeFormat),
		in.ID,
	)
	if err != nil {
		_ = tx.Rollback()
		return model.KnowledgeEntry{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO knowledge_versions(id, knowledge_id, version, content, summary, changed_by, change_note, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(),
		in.ID,
		nextVersion,
		in.Content,
		nullStringFromPtr(in.Summary),
		in.UpdatedBy,
		in.ChangeNote,
		nowUTC().Format(timeFormat),
	)
	if err != nil {
		_ = tx.Rollback()
		return model.KnowledgeEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.KnowledgeEntry{}, err
	}
	return s.GetKnowledge(ctx, in.ID)
}

func (s *Store) PatchKnowledgeMetadata(ctx context.Context, id string, summary *string, tags []string, collectionID *string, source *string, metadata map[string]any, updatedBy string, visibility *string) (model.KnowledgeEntry, error) {
	set := []string{"updated_by = ?", "updated_at = ?"}
	args := []any{updatedBy, nowUTC().Format(timeFormat)}
	if summary != nil {
		set = append(set, "summary = ?")
		args = append(args, *summary)
	}
	if tags != nil {
		set = append(set, "tags = ?")
		args = append(args, toJSON(tags))
	}
	if collectionID != nil {
		set = append(set, "collection_id = ?")
		args = append(args, collectionID)
	}
	if source != nil {
		set = append(set, "source = ?")
		args = append(args, source)
	}
	if metadata != nil {
		set = append(set, "metadata = ?")
		args = append(args, toJSON(metadata))
	}
	if visibility != nil {
		set = append(set, "visibility = ?")
		args = append(args, *visibility)
	}
	args = append(args, id)
	query := "UPDATE knowledge_entries SET " + strings.Join(set, ", ") + " WHERE id = ?"
	if _, err := s.DB.ExecContext(ctx, query, args...); err != nil {
		return model.KnowledgeEntry{}, err
	}
	return s.GetKnowledge(ctx, id)
}

func (s *Store) DeleteKnowledge(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM knowledge_entries WHERE id = ?", id)
	return err
}

func (s *Store) KnowledgeHistory(ctx context.Context, id string) ([]model.KnowledgeVersion, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, knowledge_id, version, content, summary, changed_by, change_note, created_at
FROM knowledge_versions
WHERE knowledge_id = ?
ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.KnowledgeVersion
	for rows.Next() {
		v, err := scanKnowledgeVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) KnowledgeVersion(ctx context.Context, id string, version int) (model.KnowledgeVersion, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, knowledge_id, version, content, summary, changed_by, change_note, created_at
FROM knowledge_versions
WHERE knowledge_id = ? AND version = ?`, id, version)
	return scanKnowledgeVersion(row)
}

func (s *Store) RestoreKnowledgeVersion(ctx context.Context, id string, version int, updatedBy string, changeNote *string) (model.KnowledgeEntry, error) {
	v, err := s.KnowledgeVersion(ctx, id, version)
	if err != nil {
		return model.KnowledgeEntry{}, err
	}
	return s.UpdateKnowledgeContent(ctx, UpdateKnowledgeContentInput{
		ID:         id,
		Content:    v.Content,
		Summary:    v.Summary,
		UpdatedBy:  updatedBy,
		ChangeNote: changeNote,
	})
}

func (s *Store) SetKnowledgePinned(ctx context.Context, id string, pinned bool) (model.KnowledgeEntry, error) {
	_, err := s.DB.ExecContext(ctx, "UPDATE knowledge_entries SET is_pinned = ? WHERE id = ?", boolToInt(pinned), id)
	if err != nil {
		return model.KnowledgeEntry{}, err
	}
	return s.GetKnowledge(ctx, id)
}

func (s *Store) ListKnowledgeByCollection(ctx context.Context, collectionID string, page, perPage int) ([]model.KnowledgeEntry, int, error) {
	return s.SearchKnowledge(ctx, KnowledgeFilters{
		CollectionID: collectionID,
		Page:         page,
		PerPage:      perPage,
	})
}

func scanKnowledge(scanner interface {
	Scan(dest ...any) error
}) (model.KnowledgeEntry, error) {
	var (
		e          model.KnowledgeEntry
		summary    sql.NullString
		tags       string
		collection sql.NullString
		isPinned   int
		visibility string
		source     sql.NullString
		metadata   string
		createdAt  string
		updatedAt  string
	)
	if err := scanner.Scan(
		&e.ID,
		&e.Title,
		&e.Content,
		&e.ContentType,
		&summary,
		&tags,
		&collection,
		&e.CreatedBy,
		&e.UpdatedBy,
		&e.Version,
		&e.Checksum,
		&isPinned,
		&visibility,
		&source,
		&metadata,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.KnowledgeEntry{}, err
	}
	if summary.Valid {
		e.Summary = &summary.String
	}
	e.Tags = fromJSON[[]string](tags)
	if collection.Valid {
		e.CollectionID = &collection.String
	}
	e.IsPinned = isPinned == 1
	e.Visibility = model.KnowledgeVisibility(visibility)
	if source.Valid {
		e.Source = &source.String
	}
	e.Metadata = fromJSON[map[string]any](metadata)
	e.CreatedAt = parseTS(createdAt)
	e.UpdatedAt = parseTS(updatedAt)
	return e, nil
}

func scanKnowledgeVersion(scanner interface {
	Scan(dest ...any) error
}) (model.KnowledgeVersion, error) {
	var (
		v         model.KnowledgeVersion
		summary   sql.NullString
		note      sql.NullString
		createdAt string
	)
	if err := scanner.Scan(&v.ID, &v.KnowledgeID, &v.Version, &v.Content, &summary, &v.ChangedBy, &note, &createdAt); err != nil {
		return model.KnowledgeVersion{}, err
	}
	if summary.Valid {
		v.Summary = &summary.String
	}
	if note.Valid {
		v.ChangeNote = &note.String
	}
	v.CreatedAt = parseTS(createdAt)
	return v, nil
}

func nullString(v string) sql.NullString {
	v = strings.TrimSpace(v)
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func nullStringFromPtr(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	if strings.TrimSpace(*v) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}
