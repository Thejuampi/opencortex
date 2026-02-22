package syncer

import (
	"context"
	"database/sql"
	"fmt"

	"opencortex/internal/model"
)

type ManifestItem struct {
	EntityType string `json:"entity_type"`
	ID         string `json:"id"`
	Checksum   string `json:"checksum"`
	UpdatedAt  string `json:"updated_at"`
}

func BuildManifest(ctx context.Context, db *sql.DB, scope model.SyncScope, scopeIDs []string) ([]ManifestItem, error) {
	switch scope {
	case model.SyncScopeFull:
		var out []ManifestItem
		items, err := fromKnowledge(ctx, db, "")
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		items, err = fromTopics(ctx, db)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		items, err = fromMessages(ctx, db, "")
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		return out, nil
	case model.SyncScopeCollections:
		var out []ManifestItem
		for _, id := range scopeIDs {
			items, err := fromKnowledge(ctx, db, id)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		return out, nil
	case model.SyncScopeTopics:
		return fromTopics(ctx, db)
	case model.SyncScopeMessages:
		return fromMessages(ctx, db, "")
	default:
		return nil, fmt.Errorf("unsupported scope: %s", scope)
	}
}

func fromKnowledge(ctx context.Context, db *sql.DB, collectionID string) ([]ManifestItem, error) {
	query := "SELECT id, checksum, updated_at FROM knowledge_entries"
	args := []any{}
	if collectionID != "" {
		query += " WHERE collection_id = ?"
		args = append(args, collectionID)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManifestItem
	for rows.Next() {
		var item ManifestItem
		item.EntityType = "knowledge"
		if err := rows.Scan(&item.ID, &item.Checksum, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func fromTopics(ctx context.Context, db *sql.DB) ([]ManifestItem, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, name, created_at FROM topics")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManifestItem
	for rows.Next() {
		var item ManifestItem
		var name string
		item.EntityType = "topics"
		if err := rows.Scan(&item.ID, &name, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Checksum = simpleChecksum(name)
		out = append(out, item)
	}
	return out, rows.Err()
}

func fromMessages(ctx context.Context, db *sql.DB, topicID string) ([]ManifestItem, error) {
	query := "SELECT id, content, created_at FROM messages"
	args := []any{}
	if topicID != "" {
		query += " WHERE topic_id = ?"
		args = append(args, topicID)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManifestItem
	for rows.Next() {
		var item ManifestItem
		var content string
		item.EntityType = "messages"
		if err := rows.Scan(&item.ID, &content, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Checksum = simpleChecksum(content)
		out = append(out, item)
	}
	return out, rows.Err()
}
