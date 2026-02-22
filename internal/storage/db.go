package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"opencortex/internal/config"

	_ "modernc.org/sqlite"
)

func Open(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(cfg.Database.Path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(cfg.Database.MaxConnections)
	db.SetMaxIdleConns(cfg.Database.MaxConnections)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if cfg.Database.WALMode {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set wal mode: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable fk: %w", err)
	}
	return db, nil
}

func BackupFile(srcPath, backupDir string) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir backup dir: %w", err)
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	dst := filepath.Join(backupDir, "opencortex-"+ts+".db")
	b, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read source db: %w", err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return "", fmt.Errorf("write backup db: %w", err)
	}
	return dst, nil
}
