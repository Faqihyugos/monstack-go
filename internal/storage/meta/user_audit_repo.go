package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// CreateUser inserts a new RBAC user into SQLite.
func (d *DB) CreateUser(ctx context.Context, user *models.User) error {
	if user == nil {
		return storage.ErrInvalidData
	}
	if err := user.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}

	var apiKey any
	if strings.TrimSpace(user.APIKey) != "" {
		apiKey = user.APIKey
	}

	const q = `
		INSERT INTO users (id, username, password_hash, role, api_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	_, err := d.db.ExecContext(ctx, q,
		user.ID,
		user.Username,
		user.PasswordHash,
		string(user.Role),
		apiKey,
		user.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return storage.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetUserByUsername retrieves a user by unique username.
func (d *DB) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	const q = `SELECT id, username, password_hash, role, api_key, created_at, last_login FROM users WHERE username = ?`
	u, err := scanUser(d.db.QueryRowContext(ctx, q, username))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// GetUserByAPIKey retrieves a user by unique API key.
func (d *DB) GetUserByAPIKey(ctx context.Context, apiKey string) (*models.User, error) {
	const q = `SELECT id, username, password_hash, role, api_key, created_at, last_login FROM users WHERE api_key = ?`
	u, err := scanUser(d.db.QueryRowContext(ctx, q, apiKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// ListUsers returns all RBAC users ordered by username.
func (d *DB) ListUsers(ctx context.Context) ([]*models.User, error) {
	const q = `SELECT id, username, password_hash, role, api_key, created_at, last_login FROM users ORDER BY username ASC`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	var users []*models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteUser removes a user by ID.
func (d *DB) DeleteUser(ctx context.Context, id string) error {
	const q = `DELETE FROM users WHERE id = ?`
	res, err := d.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func scanUser(s agentScanner) (*models.User, error) {
	var (
		u            models.User
		roleStr      string
		apiKey       sql.NullString
		createdStr   sql.NullString
		lastLoginStr sql.NullString
	)
	if err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &roleStr, &apiKey, &createdStr, &lastLoginStr); err != nil {
		return nil, err
	}
	u.Role = models.UserRole(roleStr)
	u.APIKey = apiKey.String
	if createdStr.Valid && createdStr.String != "" {
		u.CreatedAt = parseSQLiteTime(createdStr.String)
	}
	if lastLoginStr.Valid && lastLoginStr.String != "" {
		t := parseSQLiteTime(lastLoginStr.String)
		u.LastLogin = &t
	}
	return &u, nil
}

// RecordAudit inserts a new audit log entry.
func (d *DB) RecordAudit(ctx context.Context, entry *models.AuditEntry) error {
	if entry == nil {
		return storage.ErrInvalidData
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.Actor == "" {
		entry.Actor = "system"
	}

	const q = `
		INSERT INTO audit_log (timestamp, actor, action, resource, resource_id, details, ip_address)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	res, err := d.db.ExecContext(ctx, q,
		entry.Timestamp.UTC().Format(time.RFC3339Nano),
		entry.Actor,
		entry.Action,
		entry.Resource,
		entry.ResourceID,
		entry.Details,
		entry.IPAddress,
	)
	if err != nil {
		return fmt.Errorf("failed to record audit entry: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		entry.ID = id
	}
	return nil
}

// ListAuditLogs returns audit entries ordered newest-first, optionally filtered by resource.
func (d *DB) ListAuditLogs(ctx context.Context, resource string, limit int) ([]*models.AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id, timestamp, actor, action, resource, resource_id, details, ip_address FROM audit_log`
	var args []any
	if resource != "" {
		q += ` WHERE resource = ?`
		args = append(args, resource)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list audit logs: %w", err)
	}
	defer rows.Close()

	var entries []*models.AuditEntry
	for rows.Next() {
		var (
			e      models.AuditEntry
			tsStr  sql.NullString
			resID  sql.NullString
			detail sql.NullString
			ipAddr sql.NullString
		)
		if err := rows.Scan(&e.ID, &tsStr, &e.Actor, &e.Action, &e.Resource, &resID, &detail, &ipAddr); err != nil {
			return nil, err
		}
		if tsStr.Valid && tsStr.String != "" {
			e.Timestamp = parseSQLiteTime(tsStr.String)
		}
		e.ResourceID = resID.String
		e.Details = detail.String
		e.IPAddress = ipAddr.String
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}
