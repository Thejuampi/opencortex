package repos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"opencortex/internal/model"
)

type CreateCollectionInput struct {
	ID          string
	Name        string
	Description string
	ParentID    *string
	CreatedBy   string
	IsPublic    bool
	Metadata    map[string]any
}

func (s *Store) CreateCollection(ctx context.Context, in CreateCollectionInput) (model.Collection, error) {
	now := nowUTC().Format(timeFormat)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO collections(id, name, description, parent_id, created_by, is_public, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, in.Description, in.ParentID, in.CreatedBy, boolToInt(in.IsPublic), toJSON(in.Metadata), now, now,
	)
	if err != nil {
		return model.Collection{}, err
	}
	return s.GetCollection(ctx, in.ID)
}

func (s *Store) ListCollections(ctx context.Context, page, perPage int) ([]model.Collection, int, error) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 50
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM collections").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, description, parent_id, created_by, is_public, metadata, created_at, updated_at
FROM collections
ORDER BY updated_at DESC
LIMIT ? OFFSET ?`, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func (s *Store) GetCollection(ctx context.Context, id string) (model.Collection, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, name, description, parent_id, created_by, is_public, metadata, created_at, updated_at
FROM collections
WHERE id = ?`, id)
	return scanCollection(row)
}

func (s *Store) UpdateCollection(ctx context.Context, id string, name, description *string, parentID *string, isPublic *bool, metadata map[string]any) (model.Collection, error) {
	if parentID != nil {
		cycle, err := s.hasCollectionCycle(ctx, id, *parentID)
		if err != nil {
			return model.Collection{}, err
		}
		if cycle {
			return model.Collection{}, fmt.Errorf("collection_cycle")
		}
	}
	set := []string{"updated_at = ?"}
	args := []any{nowUTC().Format(timeFormat)}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	if parentID != nil {
		set = append(set, "parent_id = ?")
		args = append(args, parentID)
	}
	if isPublic != nil {
		set = append(set, "is_public = ?")
		args = append(args, boolToInt(*isPublic))
	}
	if metadata != nil {
		set = append(set, "metadata = ?")
		args = append(args, toJSON(metadata))
	}
	args = append(args, id)
	query := "UPDATE collections SET " + strings.Join(set, ", ") + " WHERE id = ?"
	if _, err := s.DB.ExecContext(ctx, query, args...); err != nil {
		return model.Collection{}, err
	}
	return s.GetCollection(ctx, id)
}

func (s *Store) DeleteCollection(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM collections WHERE id = ?", id)
	return err
}

func (s *Store) CollectionTree(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, description, parent_id, created_by, is_public, metadata, created_at, updated_at
FROM collections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]map[string]any{}
	children := map[string][]map[string]any{}
	var roots []map[string]any
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		node := map[string]any{
			"id":          c.ID,
			"name":        c.Name,
			"description": c.Description,
			"parent_id":   c.ParentID,
			"created_by":  c.CreatedBy,
			"is_public":   c.IsPublic,
			"metadata":    c.Metadata,
			"created_at":  c.CreatedAt,
			"updated_at":  c.UpdatedAt,
			"children":    []map[string]any{},
		}
		all[c.ID] = node
		if c.ParentID != nil {
			children[*c.ParentID] = append(children[*c.ParentID], node)
		} else {
			roots = append(roots, node)
		}
	}
	for id, node := range all {
		node["children"] = children[id]
	}
	return roots, rows.Err()
}

func (s *Store) hasCollectionCycle(ctx context.Context, id, candidateParent string) (bool, error) {
	if id == candidateParent {
		return true, nil
	}
	current := candidateParent
	for current != "" {
		var parent sql.NullString
		err := s.DB.QueryRowContext(ctx, "SELECT parent_id FROM collections WHERE id = ?", current).Scan(&parent)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !parent.Valid {
			return false, nil
		}
		if parent.String == id {
			return true, nil
		}
		current = parent.String
	}
	return false, nil
}

func scanCollection(scanner interface {
	Scan(dest ...any) error
}) (model.Collection, error) {
	var (
		c         model.Collection
		parentID  sql.NullString
		isPublic  int
		metadata  string
		createdAt string
		updatedAt string
	)
	if err := scanner.Scan(&c.ID, &c.Name, &c.Description, &parentID, &c.CreatedBy, &isPublic, &metadata, &createdAt, &updatedAt); err != nil {
		return model.Collection{}, err
	}
	if parentID.Valid {
		c.ParentID = &parentID.String
	}
	c.IsPublic = isPublic == 1
	c.Metadata = fromJSON[map[string]any](metadata)
	c.CreatedAt = parseTS(createdAt)
	c.UpdatedAt = parseTS(updatedAt)
	return c, nil
}
