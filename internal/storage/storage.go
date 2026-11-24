package storage

import (
	"context"
	"errors"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Domain sentinel errors.
var (
	ErrNotFound      = errors.New("storage: entity not found")
	ErrAlreadyExists = errors.New("storage: entity already exists")
	ErrInvalidData   = errors.New("storage: invalid entity data")
)

// MetadataStore defines the SQLite CRUD interface for metadata.
type MetadataStore interface {
	Close() error

	// Targets
	CreateTarget(ctx context.Context, target *models.Target) error
	GetTarget(ctx context.Context, id string) (*models.Target, error)
	GetTargetByName(ctx context.Context, name string) (*models.Target, error)
	ListTargets(ctx context.Context, targetType string, enabled *bool) ([]*models.Target, error)
	UpdateTarget(ctx context.Context, target *models.Target) error
	DeleteTarget(ctx context.Context, id string) error

	// Settings
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key string, value string) error

	// Alert Rules
	CreateAlertRule(ctx context.Context, rule *models.AlertRule) error
	GetAlertRule(ctx context.Context, id string) (*models.AlertRule, error)
	ListAlertRules(ctx context.Context, enabled *bool) ([]*models.AlertRule, error)
	UpdateAlertRule(ctx context.Context, rule *models.AlertRule) error
	DeleteAlertRule(ctx context.Context, id string) error

	// Alert Events
	RecordAlertEvent(ctx context.Context, event *models.AlertEvent) error
	ListAlertEvents(ctx context.Context, status string, limit int) ([]*models.AlertEvent, error)
	UpdateAlertEvent(ctx context.Context, event *models.AlertEvent) error

	// Alert Silences
	CreateSilence(ctx context.Context, silence *models.AlertSilence) error
	ListActiveSilences(ctx context.Context, now time.Time) ([]*models.AlertSilence, error)
	DeleteSilence(ctx context.Context, id string) error

	// Agents
	UpsertAgent(ctx context.Context, agent *models.Agent) error
	GetAgent(ctx context.Context, id string) (*models.Agent, error)
	ListAgents(ctx context.Context, status string) ([]*models.Agent, error)
	UpdateAgentHeartbeat(ctx context.Context, id string, lastSeen time.Time, status models.AgentStatus) error
	DeleteAgent(ctx context.Context, id string) error
}

// TSDB defines the Time-Series Database interface.
type TSDB interface {
	Close() error
	WritePoint(point models.MetricPoint) error
	WritePoints(points []models.MetricPoint) error
	QueryRange(targetID string, metric string, start, end time.Time, step time.Duration) ([]models.MetricPoint, error)
	GetLatestPoint(targetID string, metric string) (*models.MetricPoint, error)
	Prune(retention time.Duration) (int, error)
}
