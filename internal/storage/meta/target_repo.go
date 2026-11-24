package meta

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// generateID returns a random 32-character hexadecimal UUID string.
func generateID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// parseDBTime parses database timestamps safely into UTC time.Time.
func parseDBTime(val any) (time.Time, error) {
	switch v := val.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}, nil
		}
		formats := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
		}
		for _, layout := range formats {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("unable to parse time string: %s", v)
	case []byte:
		return parseDBTime(string(v))
	case nil:
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("unexpected time type: %T", val)
	}
}

// CreateTarget inserts a new target into the database.
func (d *DB) CreateTarget(ctx context.Context, target *models.Target) error {
	if target == nil {
		return fmt.Errorf("%w: target is nil", storage.ErrInvalidData)
	}

	if target.ID == "" {
		id, err := generateID()
		if err != nil {
			return err
		}
		target.ID = id
	}

	if err := target.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	now := time.Now().UTC()
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	if target.UpdatedAt.IsZero() {
		target.UpdatedAt = now
	}

	configJSON, err := json.Marshal(target.Config)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal target config: %v", storage.ErrInvalidData, err)
	}
	if len(target.Config) == 0 {
		configJSON = []byte("{}")
	}

	labelsJSON, err := json.Marshal(target.Labels)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal target labels: %v", storage.ErrInvalidData, err)
	}
	if len(target.Labels) == 0 {
		labelsJSON = []byte("{}")
	}

	enabledInt := 0
	if target.Enabled {
		enabledInt = 1
	}

	query := `
	INSERT INTO targets (id, name, type, target, interval_ms, timeout_ms, retries, config, labels, enabled, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = d.db.ExecContext(ctx, query,
		target.ID,
		target.Name,
		string(target.Type),
		target.Target,
		target.IntervalMs,
		target.TimeoutMs,
		target.Retries,
		string(configJSON),
		string(labelsJSON),
		enabledInt,
		target.CreatedAt.Format(time.RFC3339Nano),
		target.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: target %s", storage.ErrAlreadyExists, target.Name)
		}
		return fmt.Errorf("failed to insert target: %w", err)
	}

	return nil
}

// scanTarget scans a target row into a models.Target struct.
func scanTarget(scanner interface{ Scan(dest ...any) error }) (*models.Target, error) {
	var (
		t            models.Target
		typeStr      string
		configStr    string
		labelsStr    string
		enabledInt   int
		createdAtVal any
		updatedAtVal any
	)

	err := scanner.Scan(
		&t.ID,
		&t.Name,
		&typeStr,
		&t.Target,
		&t.IntervalMs,
		&t.TimeoutMs,
		&t.Retries,
		&configStr,
		&labelsStr,
		&enabledInt,
		&createdAtVal,
		&updatedAtVal,
	)
	if err != nil {
		return nil, err
	}

	t.Type = models.TargetType(typeStr)
	t.Enabled = enabledInt != 0

	t.Config = make(map[string]any)
	if strings.TrimSpace(configStr) != "" && configStr != "{}" {
		_ = json.Unmarshal([]byte(configStr), &t.Config)
	}

	t.Labels = make(map[string]string)
	if strings.TrimSpace(labelsStr) != "" && labelsStr != "{}" {
		_ = json.Unmarshal([]byte(labelsStr), &t.Labels)
	}

	if t.CreatedAt, err = parseDBTime(createdAtVal); err != nil {
		t.CreatedAt = time.Now().UTC()
	}
	if t.UpdatedAt, err = parseDBTime(updatedAtVal); err != nil {
		t.UpdatedAt = time.Now().UTC()
	}

	return &t, nil
}

// GetTarget retrieves a target by ID.
func (d *DB) GetTarget(ctx context.Context, id string) (*models.Target, error) {
	query := `
	SELECT id, name, type, target, interval_ms, timeout_ms, retries, config, labels, enabled, created_at, updated_at
	FROM targets
	WHERE id = ?
	`
	row := d.db.QueryRowContext(ctx, query, id)
	target, err := scanTarget(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: target id %s", storage.ErrNotFound, id)
		}
		return nil, fmt.Errorf("failed to get target %s: %w", id, err)
	}
	return target, nil
}

// GetTargetByName retrieves a target by unique name.
func (d *DB) GetTargetByName(ctx context.Context, name string) (*models.Target, error) {
	query := `
	SELECT id, name, type, target, interval_ms, timeout_ms, retries, config, labels, enabled, created_at, updated_at
	FROM targets
	WHERE name = ?
	`
	row := d.db.QueryRowContext(ctx, query, name)
	target, err := scanTarget(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: target name %s", storage.ErrNotFound, name)
		}
		return nil, fmt.Errorf("failed to get target %s: %w", name, err)
	}
	return target, nil
}

// ListTargets returns targets optionally filtered by type and enabled state.
func (d *DB) ListTargets(ctx context.Context, targetType string, enabled *bool) ([]*models.Target, error) {
	var (
		whereClauses []string
		args         []any
	)

	if targetType != "" {
		whereClauses = append(whereClauses, "type = ?")
		args = append(args, targetType)
	}
	if enabled != nil {
		whereClauses = append(whereClauses, "enabled = ?")
		val := 0
		if *enabled {
			val = 1
		}
		args = append(args, val)
	}

	query := `
	SELECT id, name, type, target, interval_ms, timeout_ms, retries, config, labels, enabled, created_at, updated_at
	FROM targets
	`
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	query += " ORDER BY created_at ASC"

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list targets: %w", err)
	}
	defer rows.Close()

	targets := make([]*models.Target, 0)
	for rows.Next() {
		tg, err := scanTarget(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan target row: %w", err)
		}
		targets = append(targets, tg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading target rows: %w", err)
	}

	return targets, nil
}

// UpdateTarget updates an existing target by ID.
func (d *DB) UpdateTarget(ctx context.Context, target *models.Target) error {
	if target == nil || target.ID == "" {
		return fmt.Errorf("%w: target ID is required", storage.ErrInvalidData)
	}

	if err := target.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	target.UpdatedAt = time.Now().UTC()

	configJSON, err := json.Marshal(target.Config)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal config: %v", storage.ErrInvalidData, err)
	}
	if len(target.Config) == 0 {
		configJSON = []byte("{}")
	}

	labelsJSON, err := json.Marshal(target.Labels)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal labels: %v", storage.ErrInvalidData, err)
	}
	if len(target.Labels) == 0 {
		labelsJSON = []byte("{}")
	}

	enabledInt := 0
	if target.Enabled {
		enabledInt = 1
	}

	query := `
	UPDATE targets
	SET name = ?, type = ?, target = ?, interval_ms = ?, timeout_ms = ?, retries = ?, config = ?, labels = ?, enabled = ?, updated_at = ?
	WHERE id = ?
	`

	res, err := d.db.ExecContext(ctx, query,
		target.Name,
		string(target.Type),
		target.Target,
		target.IntervalMs,
		target.TimeoutMs,
		target.Retries,
		string(configJSON),
		string(labelsJSON),
		enabledInt,
		target.UpdatedAt.Format(time.RFC3339Nano),
		target.ID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: target name %s", storage.ErrAlreadyExists, target.Name)
		}
		return fmt.Errorf("failed to update target %s: %w", target.ID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: target id %s", storage.ErrNotFound, target.ID)
	}

	return nil
}

// DeleteTarget deletes a target by ID.
func (d *DB) DeleteTarget(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM targets WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete target %s: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: target id %s", storage.ErrNotFound, id)
	}

	return nil
}
