package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// UpsertAgent inserts or updates a registered monitoring agent.
func (d *DB) UpsertAgent(ctx context.Context, agent *models.Agent) error {
	if agent == nil {
		return storage.ErrInvalidData
	}
	if err := agent.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	collectorsJSON, err := json.Marshal(agent.Collectors)
	if err != nil {
		return fmt.Errorf("failed to marshal agent collectors: %w", err)
	}
	labelsJSON, err := json.Marshal(agent.Labels)
	if err != nil {
		return fmt.Errorf("failed to marshal agent labels: %w", err)
	}

	now := time.Now().UTC()
	if agent.LastSeenAt.IsZero() {
		agent.LastSeenAt = now
	}
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	if agent.Status == "" {
		agent.Status = models.AgentStatusOnline
	}

	const q = `
		INSERT INTO agents (id, hostname, ip_address, os, arch, version, collectors, labels, last_seen_at, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			ip_address = excluded.ip_address,
			os = excluded.os,
			arch = excluded.arch,
			version = excluded.version,
			collectors = excluded.collectors,
			labels = excluded.labels,
			last_seen_at = excluded.last_seen_at,
			status = excluded.status
	`
	_, err = d.db.ExecContext(ctx, q,
		agent.ID,
		agent.Hostname,
		agent.IPAddress,
		agent.OS,
		agent.Arch,
		agent.Version,
		string(collectorsJSON),
		string(labelsJSON),
		agent.LastSeenAt.UTC().Format(time.RFC3339Nano),
		string(agent.Status),
		agent.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert agent: %w", err)
	}
	return nil
}

// GetAgent retrieves a single agent by ID.
func (d *DB) GetAgent(ctx context.Context, id string) (*models.Agent, error) {
	const q = `
		SELECT id, hostname, ip_address, os, arch, version, collectors, labels, last_seen_at, status, created_at
		FROM agents WHERE id = ?
	`
	row := d.db.QueryRowContext(ctx, q, id)
	a, err := scanAgent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return a, nil
}

// ListAgents returns all registered agents, optionally filtered by status.
func (d *DB) ListAgents(ctx context.Context, status string) ([]*models.Agent, error) {
	q := `SELECT id, hostname, ip_address, os, arch, version, collectors, labels, last_seen_at, status, created_at FROM agents`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY hostname ASC`

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	defer rows.Close()

	var agents []*models.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// UpdateAgentHeartbeat updates an agent's last_seen_at timestamp and status.
func (d *DB) UpdateAgentHeartbeat(ctx context.Context, id string, lastSeen time.Time, status models.AgentStatus) error {
	const q = `UPDATE agents SET last_seen_at = ?, status = ? WHERE id = ?`
	res, err := d.db.ExecContext(ctx, q, lastSeen.UTC().Format(time.RFC3339Nano), string(status), id)
	if err != nil {
		return fmt.Errorf("failed to update agent heartbeat: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// DeleteAgent removes a registered agent by ID.
func (d *DB) DeleteAgent(ctx context.Context, id string) error {
	const q = `DELETE FROM agents WHERE id = ?`
	res, err := d.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("failed to delete agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

type agentScanner interface {
	Scan(dest ...any) error
}

func scanAgent(s agentScanner) (*models.Agent, error) {
	var (
		a              models.Agent
		osStr, archStr sql.NullString
		verStr         sql.NullString
		collectorsJSON sql.NullString
		labelsJSON     sql.NullString
		lastSeenStr    sql.NullString
		statusStr      string
		createdStr     sql.NullString
	)

	if err := s.Scan(
		&a.ID, &a.Hostname, &a.IPAddress,
		&osStr, &archStr, &verStr,
		&collectorsJSON, &labelsJSON,
		&lastSeenStr, &statusStr, &createdStr,
	); err != nil {
		return nil, err
	}

	a.OS = osStr.String
	a.Arch = archStr.String
	a.Version = verStr.String
	a.Status = models.AgentStatus(statusStr)

	if collectorsJSON.Valid && collectorsJSON.String != "" {
		_ = json.Unmarshal([]byte(collectorsJSON.String), &a.Collectors)
	}
	if a.Collectors == nil {
		a.Collectors = []string{}
	}
	if labelsJSON.Valid && labelsJSON.String != "" {
		_ = json.Unmarshal([]byte(labelsJSON.String), &a.Labels)
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	if lastSeenStr.Valid && lastSeenStr.String != "" {
		a.LastSeenAt = parseSQLiteTime(lastSeenStr.String)
	}
	if createdStr.Valid && createdStr.String != "" {
		a.CreatedAt = parseSQLiteTime(createdStr.String)
	}
	return &a, nil
}

func parseSQLiteTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
