package models

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AlertSeverity indicates the urgency of an alert.
type AlertSeverity string

const (
	SeverityInfo     AlertSeverity = "info"
	SeverityWarning  AlertSeverity = "warning"
	SeverityCritical AlertSeverity = "critical"
)

// IsValid checks if AlertSeverity is valid.
func (s AlertSeverity) IsValid() bool {
	return s == SeverityInfo || s == SeverityWarning || s == SeverityCritical
}

// AlertStatus represents the lifecycle state of an alert.
type AlertStatus string

const (
	AlertStatusPending  AlertStatus = "pending"
	AlertStatusFiring   AlertStatus = "firing"
	AlertStatusResolved AlertStatus = "resolved"
)

// IsValid checks if AlertStatus is valid.
func (s AlertStatus) IsValid() bool {
	return s == AlertStatusPending || s == AlertStatusFiring || s == AlertStatusResolved
}

// Notification status constants for alert events.
const (
	NotificationStatusPending = "pending"
	NotificationStatusSent    = "sent"
	NotificationStatusFailed  = "failed"
	NotificationStatusSkipped = "skipped"
)

// AlertRule defines metric threshold conditions and notification routing.
type AlertRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Expression  string            `json:"expression"`
	ForDuration string            `json:"for_duration"`
	Severity    AlertSeverity     `json:"severity"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	TargetMatch string            `json:"target_match,omitempty"`
	Channels    []string          `json:"channels"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Validate verifies the alert rule definition.
func (r *AlertRule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("alert rule name cannot be empty")
	}
	if strings.TrimSpace(r.Expression) == "" {
		return errors.New("alert rule expression cannot be empty")
	}
	if !r.Severity.IsValid() {
		return errors.New("invalid alert rule severity")
	}
	if r.ForDuration != "" {
		if _, err := time.ParseDuration(r.ForDuration); err != nil {
			return errors.New("invalid for_duration format")
		}
	}
	return nil
}

// ForDurationParsed parses the rule's for_duration string into a time.Duration.
func (r *AlertRule) ForDurationParsed() time.Duration {
	if r.ForDuration == "" {
		return 0
	}
	d, err := time.ParseDuration(r.ForDuration)
	if err != nil {
		return 0
	}
	return d
}

// AlertEvent records an alert state change or notification event.
type AlertEvent struct {
	ID                  int64         `json:"id"`
	RuleID              string        `json:"rule_id"`
	RuleName            string        `json:"rule_name"`
	TargetName          string        `json:"target_name"`
	TargetAddress       string        `json:"target_address"`
	Status              AlertStatus   `json:"status"`
	Severity            AlertSeverity `json:"severity"`
	Value               float64       `json:"value"`
	Message             string        `json:"message"`
	FiredAt             time.Time     `json:"fired_at"`
	ResolvedAt          *time.Time    `json:"resolved_at,omitempty"`
	NotifiedAt          *time.Time    `json:"notified_at,omitempty"`
	NotificationChannel string        `json:"notification_channel"`
	NotificationStatus  string        `json:"notification_status"`
}

// Validate checks whether the AlertEvent is valid.
func (e *AlertEvent) Validate() error {
	if e.RuleID == "" {
		return errors.New("alert event rule_id cannot be empty")
	}
	if !e.Status.IsValid() {
		return errors.New("invalid alert event status")
	}
	if !e.Severity.IsValid() {
		return errors.New("invalid alert event severity")
	}
	if e.FiredAt.IsZero() {
		return errors.New("alert event fired_at timestamp cannot be zero")
	}
	return nil
}

// AlertSilence suppresses notifications matching given criteria during a time window.
type AlertSilence struct {
	ID        string    `json:"id"`
	Matchers  string    `json:"matchers"` // JSON string e.g. {"target_name":"prod-api"} or comma-separated key=val
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Comment   string    `json:"comment"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Validate verifies the AlertSilence fields.
func (s *AlertSilence) Validate() error {
	if s.StartsAt.IsZero() || s.EndsAt.IsZero() {
		return errors.New("silence starts_at and ends_at cannot be zero")
	}
	if !s.EndsAt.After(s.StartsAt) {
		return errors.New("silence ends_at must be after starts_at")
	}
	if strings.TrimSpace(s.Matchers) == "" {
		return errors.New("silence matchers cannot be empty")
	}
	return nil
}

// IsActive checks if the silence is currently active at time `now`.
func (s *AlertSilence) IsActive(now time.Time) bool {
	return (now.Equal(s.StartsAt) || now.After(s.StartsAt)) && now.Before(s.EndsAt)
}

// Matches checks whether the given targetName and labels match this silence.
func (s *AlertSilence) Matches(targetName string, labels map[string]string) bool {
	matchers := strings.TrimSpace(s.Matchers)
	if matchers == "" {
		return false
	}

	// Try JSON format first: {"target":"foo", "env":"prod"}
	var matchMap map[string]string
	if err := json.Unmarshal([]byte(matchers), &matchMap); err == nil && len(matchMap) > 0 {
		for k, v := range matchMap {
			if k == "target" || k == "target_name" {
				if targetName != v {
					return false
				}
			} else {
				if labels == nil || labels[k] != v {
					return false
				}
			}
		}
		return true
	}

	// Try key=val pairs separated by commas
	if strings.Contains(matchers, "=") {
		pairs := strings.Split(matchers, ",")
		matchedAny := false
		for _, pair := range pairs {
			parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
			if len(parts) == 2 {
				k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
				matchedAny = true
				if k == "target" || k == "target_name" {
					if targetName != v {
						return false
					}
				} else {
					if labels == nil || labels[k] != v {
						return false
					}
				}
			}
		}
		if matchedAny {
			return true
		}
	}

	// Fallback to substring matching on targetName
	return strings.Contains(targetName, matchers)
}
