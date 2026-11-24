package meta

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/Faqihyugos/monstack-go/migrations"
)

// RunMigrations applies all pending embedded SQL migrations sequentially within transactions.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	// Ensure schema_migrations table exists
	createTrackingTable := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version     INTEGER PRIMARY KEY,
		name        TEXT NOT NULL,
		applied_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.ExecContext(ctx, createTrackingTable); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	// Query applied migrations
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("failed to query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("failed to scan migration version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading applied migrations: %w", err)
	}

	// Read migration files from embedded FS
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations dir: %w", err)
	}

	type migrationFile struct {
		version  int
		fileName string
	}

	var files []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		// Parse version from prefix e.g. "001_initial.sql" -> 1
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		ver, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		files = append(files, migrationFile{
			version:  ver,
			fileName: entry.Name(),
		})
	}

	// Sort migrations by version ascending
	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})

	// Execute unapplied migrations
	for _, f := range files {
		if applied[f.version] {
			continue
		}

		content, err := fs.ReadFile(migrations.FS, f.fileName)
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", f.fileName, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to start transaction for migration %s: %w", f.fileName, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", f.fileName, err)
		}

		insertSQL := "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)"
		if _, err := tx.ExecContext(ctx, insertSQL, f.version, f.fileName); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", f.fileName, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %s: %w", f.fileName, err)
		}
	}

	return nil
}
