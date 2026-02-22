package repos

import (
	"context"

	"opencortex/internal/model"
)

func (s *Store) AddAuditLog(ctx context.Context, entry model.AuditLog) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO audit_logs(id, agent_id, action, resource, resource_id, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.AgentID,
		entry.Action,
		entry.Resource,
		entry.ResourceID,
		toJSON(entry.Metadata),
		entry.CreatedAt.Format(timeFormat),
	)
	return err
}

func (s *Store) Stats(ctx context.Context) (map[string]int, error) {
	tables := []string{
		"agents",
		"topics",
		"messages",
		"message_receipts",
		"knowledge_entries",
		"collections",
		"sync_manifests",
		"sync_logs",
		"sync_conflicts",
	}
	out := map[string]int{}
	for _, t := range tables {
		c, err := s.Count(ctx, t)
		if err != nil {
			return nil, err
		}
		out[t] = c
	}
	return out, nil
}
