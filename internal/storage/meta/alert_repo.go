package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yourusername/monstack-go/internal/storage"
	"github.com/yourusername/monstack-go/pkg/models"
)

// ============================================================================
// Alert Rules
// ============================================================================

// CreateAlertRule inserts a new alert rule.
func (d *DB) CreateAlertRule(ctx context.Context, rule *models.AlertRule) error {
	if rule == nil {
		return fmt.Errorf("%w: alert rule is nil", storage.ErrInvalidData)
	}

	if rule.ID == "" {
		id, err := generateID()
		if err != nil {
			return err
		}
		rule.ID = id
	}

	if err := rule.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	now := time.Now().UTC()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	if rule.UpdatedAt.IsZero() {
		rule.UpdatedAt = now
	}

	labelsJSON, err := json.Marshal(rule.Labels)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal rule labels: %v", storage.ErrInvalidData, err)
	}
	if len(rule.Labels) == 0 {
		labelsJSON = []byte("{}")
	}

	annJSON, err := json.Marshal(rule.Annotations)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal rule annotations: %v", storage.ErrInvalidData, err)
	}
	if len(rule.Annotations) == 0 {
		annJSON = []byte("{}")
	}

	channelsJSON, err := json.Marshal(rule.Channels)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal rule channels: %v", storage.ErrInvalidData, err)
	}
	if len(rule.Channels) == 0 {
		channelsJSON = []byte("[]")
	}

	enabledInt := 0
	if rule.Enabled {
		enabledInt = 1
	}

	forDuration := rule.ForDuration
	if forDuration == "" {
		forDuration = "0s"
	}

	query := `
	INSERT INTO alert_rules (id, name, expression, for_duration, severity, labels, annotations, target_match, channels, enabled, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = d.db.ExecContext(ctx, query,
		rule.ID,
		rule.Name,
		rule.Expression,
		forDuration,
		string(rule.Severity),
		string(labelsJSON),
		string(annJSON),
		rule.TargetMatch,
		string(channelsJSON),
		enabledInt,
		rule.CreatedAt.Format(time.RFC3339Nano),
		rule.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: alert rule %s", storage.ErrAlreadyExists, rule.Name)
		}
		return fmt.Errorf("failed to insert alert rule: %w", err)
	}

	return nil
}

// scanAlertRule scans an alert rule row.
func scanAlertRule(scanner interface{ Scan(dest ...any) error }) (*models.AlertRule, error) {
	var (
		r            models.AlertRule
		severityStr  string
		labelsStr    string
		annStr       string
		channelsStr  string
		enabledInt   int
		createdAtVal any
		updatedAtVal any
	)

	err := scanner.Scan(
		&r.ID,
		&r.Name,
		&r.Expression,
		&r.ForDuration,
		&severityStr,
		&labelsStr,
		&annStr,
		&r.TargetMatch,
		&channelsStr,
		&enabledInt,
		&createdAtVal,
		&updatedAtVal,
	)
	if err != nil {
		return nil, err
	}

	r.Severity = models.AlertSeverity(severityStr)
	r.Enabled = enabledInt != 0

	r.Labels = make(map[string]string)
	if strings.TrimSpace(labelsStr) != "" && labelsStr != "{}" {
		_ = json.Unmarshal([]byte(labelsStr), &r.Labels)
	}

	r.Annotations = make(map[string]string)
	if strings.TrimSpace(annStr) != "" && annStr != "{}" {
		_ = json.Unmarshal([]byte(annStr), &r.Annotations)
	}

	r.Channels = make([]string, 0)
	if strings.TrimSpace(channelsStr) != "" && channelsStr != "[]" {
		_ = json.Unmarshal([]byte(channelsStr), &r.Channels)
	}

	if r.CreatedAt, err = parseDBTime(createdAtVal); err != nil {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt, err = parseDBTime(updatedAtVal); err != nil {
		r.UpdatedAt = time.Now().UTC()
	}

	return &r, nil
}

// GetAlertRule retrieves an alert rule by ID.
func (d *DB) GetAlertRule(ctx context.Context, id string) (*models.AlertRule, error) {
	query := `
	SELECT id, name, expression, for_duration, severity, labels, annotations, target_match, channels, enabled, created_at, updated_at
	FROM alert_rules
	WHERE id = ?
	`
	row := d.db.QueryRowContext(ctx, query, id)
	rule, err := scanAlertRule(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: alert rule %s", storage.ErrNotFound, id)
		}
		return nil, fmt.Errorf("failed to get alert rule %s: %w", id, err)
	}
	return rule, nil
}

// ListAlertRules retrieves all alert rules, optionally filtered by enabled.
func (d *DB) ListAlertRules(ctx context.Context, enabled *bool) ([]*models.AlertRule, error) {
	var (
		whereClauses []string
		args         []any
	)

	if enabled != nil {
		whereClauses = append(whereClauses, "enabled = ?")
		val := 0
		if *enabled {
			val = 1
		}
		args = append(args, val)
	}

	query := `
	SELECT id, name, expression, for_duration, severity, labels, annotations, target_match, channels, enabled, created_at, updated_at
	FROM alert_rules
	`
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	query += " ORDER BY created_at ASC"

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list alert rules: %w", err)
	}
	defer rows.Close()

	rules := make([]*models.AlertRule, 0)
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan alert rule row: %w", err)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading alert rules rows: %w", err)
	}

	return rules, nil
}

// UpdateAlertRule updates an existing alert rule.
func (d *DB) UpdateAlertRule(ctx context.Context, rule *models.AlertRule) error {
	if rule == nil || rule.ID == "" {
		return fmt.Errorf("%w: alert rule ID is required", storage.ErrInvalidData)
	}

	if err := rule.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	rule.UpdatedAt = time.Now().UTC()

	labelsJSON, err := json.Marshal(rule.Labels)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal labels: %v", storage.ErrInvalidData, err)
	}
	if len(rule.Labels) == 0 {
		labelsJSON = []byte("{}")
	}

	annJSON, err := json.Marshal(rule.Annotations)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal annotations: %v", storage.ErrInvalidData, err)
	}
	if len(rule.Annotations) == 0 {
		annJSON = []byte("{}")
	}

	channelsJSON, err := json.Marshal(rule.Channels)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal channels: %v", storage.ErrInvalidData, err)
	}
	if len(rule.Channels) == 0 {
		channelsJSON = []byte("[]")
	}

	enabledInt := 0
	if rule.Enabled {
		enabledInt = 1
	}

	query := `
	UPDATE alert_rules
	SET name = ?, expression = ?, for_duration = ?, severity = ?, labels = ?, annotations = ?, target_match = ?, channels = ?, enabled = ?, updated_at = ?
	WHERE id = ?
	`

	res, err := d.db.ExecContext(ctx, query,
		rule.Name,
		rule.Expression,
		rule.ForDuration,
		string(rule.Severity),
		string(labelsJSON),
		string(annJSON),
		rule.TargetMatch,
		string(channelsJSON),
		enabledInt,
		rule.UpdatedAt.Format(time.RFC3339Nano),
		rule.ID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: alert rule name %s", storage.ErrAlreadyExists, rule.Name)
		}
		return fmt.Errorf("failed to update alert rule %s: %w", rule.ID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: alert rule %s", storage.ErrNotFound, rule.ID)
	}

	return nil
}

// DeleteAlertRule deletes an alert rule by ID.
func (d *DB) DeleteAlertRule(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM alert_rules WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete alert rule %s: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: alert rule %s", storage.ErrNotFound, id)
	}

	return nil
}

// ============================================================================
// Alert Events
// ============================================================================

// RecordAlertEvent logs an alert event into the database and sets event.ID.
func (d *DB) RecordAlertEvent(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return fmt.Errorf("%w: alert event is nil", storage.ErrInvalidData)
	}

	if event.FiredAt.IsZero() {
		event.FiredAt = time.Now().UTC()
	}

	if err := event.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	var resolvedAtStr any = nil
	if event.ResolvedAt != nil && !event.ResolvedAt.IsZero() {
		resolvedAtStr = event.ResolvedAt.UTC().Format(time.RFC3339Nano)
	}

	var notifiedAtStr any = nil
	if event.NotifiedAt != nil && !event.NotifiedAt.IsZero() {
		notifiedAtStr = event.NotifiedAt.UTC().Format(time.RFC3339Nano)
	}

	notifStatus := event.NotificationStatus
	if notifStatus == "" {
		notifStatus = models.NotificationStatusPending
	}

	query := `
	INSERT INTO alert_events (rule_id, rule_name, target_name, target_address, status, severity, value, message, fired_at, resolved_at, notified_at, notification_channel, notification_status)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	res, err := d.db.ExecContext(ctx, query,
		event.RuleID,
		event.RuleName,
		event.TargetName,
		event.TargetAddress,
		string(event.Status),
		string(event.Severity),
		event.Value,
		event.Message,
		event.FiredAt.UTC().Format(time.RFC3339Nano),
		resolvedAtStr,
		notifiedAtStr,
		event.NotificationChannel,
		notifStatus,
	)
	if err != nil {
		return fmt.Errorf("failed to record alert event: %w", err)
	}

	id, err := res.LastInsertId()
	if err == nil {
		event.ID = id
	}

	return nil
}

// scanAlertEvent scans an alert event row.
func scanAlertEvent(scanner interface{ Scan(dest ...any) error }) (*models.AlertEvent, error) {
	var (
		e             models.AlertEvent
		statusStr     string
		severityStr   string
		firedAtVal    any
		resolvedAtVal any
		notifiedAtVal any
	)

	err := scanner.Scan(
		&e.ID,
		&e.RuleID,
		&e.RuleName,
		&e.TargetName,
		&e.TargetAddress,
		&statusStr,
		&severityStr,
		&e.Value,
		&e.Message,
		&firedAtVal,
		&resolvedAtVal,
		&notifiedAtVal,
		&e.NotificationChannel,
		&e.NotificationStatus,
	)
	if err != nil {
		return nil, err
	}

	e.Status = models.AlertStatus(statusStr)
	e.Severity = models.AlertSeverity(severityStr)

	if e.FiredAt, err = parseDBTime(firedAtVal); err != nil {
		e.FiredAt = time.Now().UTC()
	}

	if resolvedAtVal != nil {
		if t, err := parseDBTime(resolvedAtVal); err == nil && !t.IsZero() {
			e.ResolvedAt = &t
		}
	}

	if notifiedAtVal != nil {
		if t, err := parseDBTime(notifiedAtVal); err == nil && !t.IsZero() {
			e.NotifiedAt = &t
		}
	}

	return &e, nil
}

// ListAlertEvents retrieves alert events, optionally filtered by status and limited.
func (d *DB) ListAlertEvents(ctx context.Context, status string, limit int) ([]*models.AlertEvent, error) {
	var (
		whereClauses []string
		args         []any
	)

	if status != "" {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, status)
	}

	query := `
	SELECT id, rule_id, rule_name, target_name, target_address, status, severity, value, message, fired_at, resolved_at, notified_at, notification_channel, notification_status
	FROM alert_events
	`
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	query += " ORDER BY fired_at DESC, id DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list alert events: %w", err)
	}
	defer rows.Close()

	events := make([]*models.AlertEvent, 0)
	for rows.Next() {
		ev, err := scanAlertEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan alert event row: %w", err)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading alert event rows: %w", err)
	}

	return events, nil
}

// UpdateAlertEvent updates an existing alert event.
func (d *DB) UpdateAlertEvent(ctx context.Context, event *models.AlertEvent) error {
	if event == nil || event.ID == 0 {
		return fmt.Errorf("%w: alert event ID is required", storage.ErrInvalidData)
	}

	var resolvedAtStr any = nil
	if event.ResolvedAt != nil && !event.ResolvedAt.IsZero() {
		resolvedAtStr = event.ResolvedAt.UTC().Format(time.RFC3339Nano)
	}

	var notifiedAtStr any = nil
	if event.NotifiedAt != nil && !event.NotifiedAt.IsZero() {
		notifiedAtStr = event.NotifiedAt.UTC().Format(time.RFC3339Nano)
	}

	query := `
	UPDATE alert_events
	SET status = ?, severity = ?, value = ?, message = ?, resolved_at = ?, notified_at = ?, notification_channel = ?, notification_status = ?
	WHERE id = ?
	`

	res, err := d.db.ExecContext(ctx, query,
		string(event.Status),
		string(event.Severity),
		event.Value,
		event.Message,
		resolvedAtStr,
		notifiedAtStr,
		event.NotificationChannel,
		event.NotificationStatus,
		event.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update alert event %d: %w", event.ID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: alert event %d", storage.ErrNotFound, event.ID)
	}

	return nil
}

// ============================================================================
// Alert Silences
// ============================================================================

// CreateSilence registers a new maintenance silence window.
func (d *DB) CreateSilence(ctx context.Context, silence *models.AlertSilence) error {
	if silence == nil {
		return fmt.Errorf("%w: silence is nil", storage.ErrInvalidData)
	}

	if silence.ID == "" {
		id, err := generateID()
		if err != nil {
			return err
		}
		silence.ID = id
	}

	if err := silence.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	if silence.CreatedAt.IsZero() {
		silence.CreatedAt = time.Now().UTC()
	}

	query := `
	INSERT INTO alert_silences (id, matchers, starts_at, ends_at, comment, created_by, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err := d.db.ExecContext(ctx, query,
		silence.ID,
		silence.Matchers,
		silence.StartsAt.UTC().Format(time.RFC3339Nano),
		silence.EndsAt.UTC().Format(time.RFC3339Nano),
		silence.Comment,
		silence.CreatedBy,
		silence.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("failed to insert alert silence: %w", err)
	}

	return nil
}

// scanSilence scans an alert silence row.
func scanSilence(scanner interface{ Scan(dest ...any) error }) (*models.AlertSilence, error) {
	var (
		s            models.AlertSilence
		startsAtVal  any
		endsAtVal    any
		createdAtVal any
	)

	err := scanner.Scan(
		&s.ID,
		&s.Matchers,
		&startsAtVal,
		&endsAtVal,
		&s.Comment,
		&s.CreatedBy,
		&createdAtVal,
	)
	if err != nil {
		return nil, err
	}

	if s.StartsAt, err = parseDBTime(startsAtVal); err != nil {
		return nil, fmt.Errorf("invalid silence starts_at: %w", err)
	}
	if s.EndsAt, err = parseDBTime(endsAtVal); err != nil {
		return nil, fmt.Errorf("invalid silence ends_at: %w", err)
	}
	if s.CreatedAt, err = parseDBTime(createdAtVal); err != nil {
		s.CreatedAt = time.Now().UTC()
	}

	return &s, nil
}

// ListActiveSilences returns silences that are active at time `now`.
func (d *DB) ListActiveSilences(ctx context.Context, now time.Time) ([]*models.AlertSilence, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	query := `
	SELECT id, matchers, starts_at, ends_at, comment, created_by, created_at
	FROM alert_silences
	WHERE starts_at <= ? AND ends_at > ?
	ORDER BY starts_at ASC
	`

	nowStr := now.UTC().Format(time.RFC3339Nano)
	rows, err := d.db.QueryContext(ctx, query, nowStr, nowStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query active silences: %w", err)
	}
	defer rows.Close()

	active := make([]*models.AlertSilence, 0)
	for rows.Next() {
		s, err := scanSilence(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan silence row: %w", err)
		}
		if s.IsActive(now) {
			active = append(active, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading active silences: %w", err)
	}

	return active, nil
}

// DeleteSilence deletes an alert silence by ID.
func (d *DB) DeleteSilence(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM alert_silences WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete alert silence %s: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: alert silence %s", storage.ErrNotFound, id)
	}

	return nil
}
