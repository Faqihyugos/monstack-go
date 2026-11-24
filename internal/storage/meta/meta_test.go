package meta_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yourusername/monstack-go/internal/config"
	"github.com/yourusername/monstack-go/internal/storage"
	"github.com/yourusername/monstack-go/internal/storage/meta"
	"github.com/yourusername/monstack-go/pkg/models"
)

func newTestDB(t *testing.T) (*meta.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "metadata_test.db")

	cfg := config.MetadataConfig{
		Path:        dbPath,
		WALMode:     true,
		BusyTimeout: 5 * time.Second,
	}

	db, err := meta.Open(cfg)
	require.NoError(t, err)

	cleanup := func() {
		_ = db.Close()
		_ = os.RemoveAll(dir)
	}

	return db, cleanup
}

func TestMigrations(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Settings should be functional
	err := db.SetSetting(ctx, "app_version", "1.0.0")
	require.NoError(t, err)

	val, err := db.GetSetting(ctx, "app_version")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", val)

	_, err = db.GetSetting(ctx, "missing_key")
	assert.ErrorIs(t, err, storage.ErrNotFound)
}

func TestTargetCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Create Target
	tg := &models.Target{
		Name:       "api-service",
		Type:       models.TargetTypeHTTP,
		Target:     "http://127.0.0.1:8080/health",
		IntervalMs: 5000,
		TimeoutMs:  1000,
		Retries:    3,
		Config: map[string]any{
			"method": "GET",
		},
		Labels: map[string]string{
			"env": "prod",
		},
		Enabled: true,
	}

	err := db.CreateTarget(ctx, tg)
	require.NoError(t, err)
	assert.NotEmpty(t, tg.ID)
	assert.False(t, tg.CreatedAt.IsZero())
	assert.False(t, tg.UpdatedAt.IsZero())

	// 2. Duplicate Name
	dupTg := &models.Target{
		Name:       "api-service",
		Type:       models.TargetTypeHTTP,
		Target:     "http://127.0.0.1:8080/health",
		IntervalMs: 5000,
		TimeoutMs:  1000,
	}
	err = db.CreateTarget(ctx, dupTg)
	assert.ErrorIs(t, err, storage.ErrAlreadyExists)

	// 3. Get Target by ID
	fetched, err := db.GetTarget(ctx, tg.ID)
	require.NoError(t, err)
	assert.Equal(t, tg.ID, fetched.ID)
	assert.Equal(t, "api-service", fetched.Name)
	assert.Equal(t, models.TargetTypeHTTP, fetched.Type)
	assert.Equal(t, "GET", fetched.Config["method"])
	assert.Equal(t, "prod", fetched.Labels["env"])
	assert.True(t, fetched.Enabled)

	// 4. Get Target by Name
	fetchedByName, err := db.GetTargetByName(ctx, "api-service")
	require.NoError(t, err)
	assert.Equal(t, tg.ID, fetchedByName.ID)

	// 5. List Targets
	list, err := db.ListTargets(ctx, "", nil)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// Filter by type
	httpList, err := db.ListTargets(ctx, "http", nil)
	require.NoError(t, err)
	assert.Len(t, httpList, 1)

	tcpList, err := db.ListTargets(ctx, "tcp", nil)
	require.NoError(t, err)
	assert.Empty(t, tcpList)

	// Filter by enabled
	enabledTrue := true
	enabledList, err := db.ListTargets(ctx, "", &enabledTrue)
	require.NoError(t, err)
	assert.Len(t, enabledList, 1)

	enabledFalse := false
	disabledList, err := db.ListTargets(ctx, "", &enabledFalse)
	require.NoError(t, err)
	assert.Empty(t, disabledList)

	// 6. Update Target
	fetched.Target = "https://example.com/health"
	fetched.IntervalMs = 15000
	fetched.Enabled = false
	err = db.UpdateTarget(ctx, fetched)
	require.NoError(t, err)

	updated, err := db.GetTarget(ctx, tg.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/health", updated.Target)
	assert.Equal(t, 15000, updated.IntervalMs)
	assert.False(t, updated.Enabled)

	// 7. Delete Target
	err = db.DeleteTarget(ctx, tg.ID)
	require.NoError(t, err)

	_, err = db.GetTarget(ctx, tg.ID)
	assert.ErrorIs(t, err, storage.ErrNotFound)

	// Delete non-existent
	err = db.DeleteTarget(ctx, tg.ID)
	assert.ErrorIs(t, err, storage.ErrNotFound)
}

func TestAlertRulesAndEventsCascade(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Create Alert Rule
	rule := &models.AlertRule{
		Name:        "HighLatency",
		Expression:  "probe_duration_seconds > 0.5",
		ForDuration: "30s",
		Severity:    models.SeverityWarning,
		Labels: map[string]string{
			"team": "ops",
		},
		Channels: []string{"telegram"},
		Enabled:  true,
	}

	err := db.CreateAlertRule(ctx, rule)
	require.NoError(t, err)
	assert.NotEmpty(t, rule.ID)

	// Duplicate rule name
	dupRule := &models.AlertRule{
		Name:       "HighLatency",
		Expression: "probe_duration_seconds > 1.0",
		Severity:   models.SeverityCritical,
	}
	err = db.CreateAlertRule(ctx, dupRule)
	assert.ErrorIs(t, err, storage.ErrAlreadyExists)

	// 2. Get and List Rules
	fetchedRule, err := db.GetAlertRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "HighLatency", fetchedRule.Name)
	assert.Equal(t, []string{"telegram"}, fetchedRule.Channels)

	rules, err := db.ListAlertRules(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, rules, 1)

	// 3. Record Alert Event
	now := time.Now().UTC()
	event := &models.AlertEvent{
		RuleID:              rule.ID,
		RuleName:            rule.Name,
		TargetName:          "prod-api",
		TargetAddress:       "http://127.0.0.1:8080",
		Status:              models.AlertStatusFiring,
		Severity:            models.SeverityWarning,
		Value:               0.85,
		Message:             "Latency exceeded 0.5s",
		FiredAt:             now,
		NotificationChannel: "telegram",
		NotificationStatus:  models.NotificationStatusSent,
	}

	err = db.RecordAlertEvent(ctx, event)
	require.NoError(t, err)
	assert.NotZero(t, event.ID)

	// List Events
	events, err := db.ListAlertEvents(ctx, string(models.AlertStatusFiring), 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, event.ID, events[0].ID)
	assert.Equal(t, "HighLatency", events[0].RuleName)

	// Update Event to resolved
	resolvedAt := now.Add(2 * time.Minute)
	event.Status = models.AlertStatusResolved
	event.ResolvedAt = &resolvedAt
	err = db.UpdateAlertEvent(ctx, event)
	require.NoError(t, err)

	eventsResolved, err := db.ListAlertEvents(ctx, string(models.AlertStatusResolved), 10)
	require.NoError(t, err)
	require.Len(t, eventsResolved, 1)
	require.NotNil(t, eventsResolved[0].ResolvedAt)

	// 4. Test Cascade Delete: Deleting the Rule MUST cascade-delete its events!
	err = db.DeleteAlertRule(ctx, rule.ID)
	require.NoError(t, err)

	allEvents, err := db.ListAlertEvents(ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, allEvents, "alert events should be cascade deleted when parent rule is deleted")
}

func TestAlertSilences(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	now := time.Now().UTC()
	silence := &models.AlertSilence{
		Matchers:  `{"target":"prod-api"}`,
		StartsAt:  now.Add(-1 * time.Hour),
		EndsAt:    now.Add(1 * time.Hour),
		Comment:   "Maintenance",
		CreatedBy: "admin",
	}

	err := db.CreateSilence(ctx, silence)
	require.NoError(t, err)
	assert.NotEmpty(t, silence.ID)

	// Query active silences at now
	active, err := db.ListActiveSilences(ctx, now)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, silence.ID, active[0].ID)

	// Query at future time outside window
	inactive, err := db.ListActiveSilences(ctx, now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, inactive)

	// Delete silence
	err = db.DeleteSilence(ctx, silence.ID)
	require.NoError(t, err)

	activeAfterDelete, err := db.ListActiveSilences(ctx, now)
	require.NoError(t, err)
	assert.Empty(t, activeAfterDelete)
}

func TestConcurrentReadsAndWrites(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	concurrency := 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()
			tg := &models.Target{
				Name:       fmt.Sprintf("target-%d", id),
				Type:       models.TargetTypeHTTP,
				Target:     fmt.Sprintf("http://127.0.0.1:%d/health", 8000+id),
				IntervalMs: 5000,
				TimeoutMs:  1000,
				Enabled:    true,
			}
			err := db.CreateTarget(ctx, tg)
			assert.NoError(t, err)

			fetched, err := db.GetTarget(ctx, tg.ID)
			assert.NoError(t, err)
			assert.Equal(t, tg.Name, fetched.Name)

			list, err := db.ListTargets(ctx, "", nil)
			assert.NoError(t, err)
			assert.NotEmpty(t, list)
		}(i)
	}

	wg.Wait()

	list, err := db.ListTargets(ctx, "", nil)
	require.NoError(t, err)
	assert.Len(t, list, concurrency)
}
