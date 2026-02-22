package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	DB *sql.DB
}

const timeFormat = time.RFC3339Nano

func New(db *sql.DB) *Store {
	return &Store{DB: db}
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func newID() string {
	return uuid.NewString()
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fromJSON[T any](s string) T {
	var v T
	if strings.TrimSpace(s) == "" {
		return v
	}
	_ = json.Unmarshal([]byte(s), &v)
	return v
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func parseTSPtr(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Store) Count(ctx context.Context, table string) (int, error) {
	var c int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if err := s.DB.QueryRowContext(ctx, query).Scan(&c); err != nil {
		return 0, err
	}
	return c, nil
}
