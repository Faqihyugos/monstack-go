package meta

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/storage"
)

// DB represents the SQLite metadata storage engine.
type DB struct {
	db *sql.DB
}

// Ensure DB implements storage.MetadataStore.
var _ storage.MetadataStore = (*DB)(nil)

// Open initializes SQLite metadata database, configures pragmas, pool, and runs migrations.
func Open(cfg config.MetadataConfig) (*DB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("metadata database path is required")
	}

	isMemory := cfg.Path == ":memory:" || strings.Contains(cfg.Path, "mode=memory")
	if !isMemory {
		if dir := filepath.Dir(cfg.Path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
			}
		}
	}

	busyTimeoutMs := 5000
	if cfg.BusyTimeout > 0 {
		busyTimeoutMs = int(cfg.BusyTimeout.Milliseconds())
	}

	separator := "?"
	if strings.Contains(cfg.Path, "?") {
		separator = "&"
	}

	dsn := fmt.Sprintf("%s%s_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)", cfg.Path, separator, busyTimeoutMs)
	if !isMemory {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Single connection pool prevents database lock concurrency issues with pure Go SQLite
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping sqlite database: %w", err)
	}

	// Explicit PRAGMAs for defense-in-depth
	_, _ = db.ExecContext(ctx, "PRAGMA foreign_keys = ON;")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d;", busyTimeoutMs))
	if !isMemory {
		_, _ = db.ExecContext(ctx, "PRAGMA journal_mode = WAL;")
	}

	// Apply migrations
	if err := RunMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	return &DB{db: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// GetSetting retrieves a setting value by key.
func (d *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := d.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: setting %s", storage.ErrNotFound, key)
		}
		return "", fmt.Errorf("failed to get setting %s: %w", key, err)
	}
	return val, nil
}

// SetSetting inserts or updates a key-value setting.
func (d *DB) SetSetting(ctx context.Context, key string, value string) error {
	query := `
	INSERT INTO settings (key, value, updated_at) 
	VALUES (?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`
	_, err := d.db.ExecContext(ctx, query, key, value)
	if err != nil {
		return fmt.Errorf("failed to set setting %s: %w", key, err)
	}
	return nil
}
