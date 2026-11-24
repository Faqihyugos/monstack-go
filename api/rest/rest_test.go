package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yourusername/monstack-go/api/rest"
	"github.com/yourusername/monstack-go/internal/config"
	"github.com/yourusername/monstack-go/internal/prober"
	"github.com/yourusername/monstack-go/internal/storage/meta"
	"github.com/yourusername/monstack-go/internal/storage/tsdb"
	"github.com/yourusername/monstack-go/pkg/health"
	"github.com/yourusername/monstack-go/pkg/metrics"
	"github.com/yourusername/monstack-go/pkg/models"
)

type testRig struct {
	server      *rest.Server
	db          *meta.DB
	tsdb        *tsdb.TSDB
	engine      *prober.DefaultEngine
	health      *health.Registry
	metrics     *metrics.Metrics
	cfg         *config.Config
	reloadCalls int
}

func setupTestRig(t *testing.T, modifyCfg ...func(*config.Config)) (*testRig, func()) {
	t.Helper()
	tempDir := t.TempDir()

	dbPath := filepath.Join(tempDir, "test_meta.db")
	db, err := meta.Open(config.MetadataConfig{
		Path:        dbPath,
		WALMode:     true,
		BusyTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	tsdbDir := filepath.Join(tempDir, "tsdb")
	require.NoError(t, os.MkdirAll(tsdbDir, 0755))
	tsdbStore, err := tsdb.Open(config.TSDBConfig{
		Path:      tsdbDir,
		Retention: "7d",
		MaxDisk:   "10GB",
	})
	require.NoError(t, err)

	m := metrics.NewMetrics()
	engine := prober.NewDefaultEngine(
		prober.WithTSDB(tsdbStore),
		prober.WithMetrics(m),
	)

	hReg := health.NewRegistry("v1.2.3")
	_ = hReg.Register("storage", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{
			Status:    health.StatusUP,
			CheckedAt: time.Now().UTC(),
		}
	})
	_ = hReg.Register("engine", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{
			Status:    health.StatusUP,
			CheckedAt: time.Now().UTC(),
		}
	})

	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen:       ":8080",
			Mode:         "http",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		Auth: config.AuthConfig{
			Enabled:       false,
			AdminUser:     "admin",
			AdminPassword: "secret-password",
		},
	}
	for _, fn := range modifyCfg {
		fn(cfg)
	}

	rig := &testRig{
		db:      db,
		tsdb:    tsdbStore,
		engine:  engine,
		health:  hReg,
		metrics: m,
		cfg:     cfg,
	}

	handlers := rest.NewHandlers(
		rest.WithMetaStore(db),
		rest.WithTSDB(tsdbStore),
		rest.WithProberEngine(engine),
		rest.WithConfig(cfg),
		rest.WithHealth(hReg),
		rest.WithMetrics(m),
		rest.WithReloadFunc(func(ctx context.Context) error {
			rig.reloadCalls++
			return nil
		}),
	)

	server := rest.NewServer(
		rest.WithHandlers(handlers),
		rest.WithServerConfig(cfg),
	)
	rig.server = server

	cleanup := func() {
		_ = db.Close()
		_ = tsdbStore.Close()
	}

	return rig, cleanup
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body any, authHeader ...string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(b)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if len(authHeader) > 0 && authHeader[0] != "" {
		req.Header.Set("Authorization", authHeader[0])
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// 1. Health Endpoint Tests
func TestHealthEndpoint(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	rec := doRequest(t, rig.server, http.MethodGet, "/api/v1/health", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var res struct {
		Status        string         `json:"status"`
		Version       string         `json:"version"`
		Uptime        string         `json:"uptime"`
		UptimeSeconds float64        `json:"uptime_seconds"`
		Subsystems    map[string]any `json:"subsystems"`
	}
	err := json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)

	assert.Equal(t, "up", res.Status)
	assert.Equal(t, "v1.2.3", res.Version)
	assert.NotEmpty(t, res.Uptime)
	assert.NotEmpty(t, res.Subsystems)
	assert.Contains(t, res.Subsystems, "storage")
	assert.Contains(t, res.Subsystems, "engine")

	// Test degraded/down status propagation
	_ = rig.health.Register("failing_component", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{
			Status: health.StatusDOWN,
			Error:  "connection refused",
		}
	})
	recDown := doRequest(t, rig.server, http.MethodGet, "/api/v1/health", nil)
	assert.Equal(t, http.StatusServiceUnavailable, recDown.Code)
	var resDown struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(recDown.Body.Bytes(), &resDown)
	assert.Equal(t, "down", resDown.Status)
}

// 2. Metrics Exposition Endpoint Tests
func TestMetricsEndpoint(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	rec := doRequest(t, rig.server, http.MethodGet, "/metrics", nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "# HELP")
	assert.Contains(t, body, "# TYPE")
	assert.Contains(t, body, "monstack_")
}

// 3. Web Dashboard Tests
func TestWebDashboard(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	rec := doRequest(t, rig.server, http.MethodGet, "/", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")

	body := rec.Body.String()
	assert.True(t, strings.Contains(body, "<!DOCTYPE html>") || strings.Contains(body, "<html"))
	assert.Contains(t, strings.ToLower(body), "monstack")
}

// 4. Targets CRUD Endpoints Tests
func TestTargetsCRUD(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	// Initially empty list
	recList := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets", nil)
	assert.Equal(t, http.StatusOK, recList.Code)
	var emptyList struct {
		Targets []*models.Target `json:"targets"`
	}
	require.NoError(t, json.Unmarshal(recList.Body.Bytes(), &emptyList))
	assert.Empty(t, emptyList.Targets)

	// Create Target
	targetPayload := map[string]any{
		"id":          "tgt-unit-1",
		"name":        "unit-target-1",
		"type":        "http",
		"target":      "http://127.0.0.1:8080/test",
		"interval_ms": 10000,
		"timeout_ms":  2000,
		"enabled":     true,
		"labels": map[string]string{
			"env": "test",
		},
	}
	recCreate := doRequest(t, rig.server, http.MethodPost, "/api/v1/targets", targetPayload)
	assert.Equal(t, http.StatusCreated, recCreate.Code)

	var createdTarget models.Target
	require.NoError(t, json.Unmarshal(recCreate.Body.Bytes(), &createdTarget))
	assert.Equal(t, "tgt-unit-1", createdTarget.ID)
	assert.Equal(t, "unit-target-1", createdTarget.Name)
	assert.Equal(t, models.TargetTypeHTTP, createdTarget.Type)

	// Duplicate Creation should return 409 Conflict
	recDup := doRequest(t, rig.server, http.MethodPost, "/api/v1/targets", targetPayload)
	assert.Equal(t, http.StatusConflict, recDup.Code)

	// Invalid target payload should return 400 Bad Request
	recInvalid := doRequest(t, rig.server, http.MethodPost, "/api/v1/targets", map[string]any{
		"name":   "",
		"type":   "unknown_type",
		"target": "",
	})
	assert.Equal(t, http.StatusBadRequest, recInvalid.Code)

	// Get Target by ID
	recGet := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/tgt-unit-1", nil)
	assert.Equal(t, http.StatusOK, recGet.Code)
	var fetchedTarget models.Target
	require.NoError(t, json.Unmarshal(recGet.Body.Bytes(), &fetchedTarget))
	assert.Equal(t, "unit-target-1", fetchedTarget.Name)

	// Get Non-Existent Target should return 404
	recNotFound := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/non-existent", nil)
	assert.Equal(t, http.StatusNotFound, recNotFound.Code)

	// Update Target (PUT)
	updatePayload := map[string]any{
		"name":        "unit-target-1-updated",
		"interval_ms": 15000,
		"enabled":     false,
	}
	recUpdate := doRequest(t, rig.server, http.MethodPut, "/api/v1/targets/tgt-unit-1", updatePayload)
	assert.Equal(t, http.StatusOK, recUpdate.Code)
	var updatedTarget models.Target
	require.NoError(t, json.Unmarshal(recUpdate.Body.Bytes(), &updatedTarget))
	assert.Equal(t, "unit-target-1-updated", updatedTarget.Name)
	assert.Equal(t, 15000, updatedTarget.IntervalMs)
	assert.False(t, updatedTarget.Enabled)

	// Update Non-Existent Target should return 404
	recUpdate404 := doRequest(t, rig.server, http.MethodPut, "/api/v1/targets/non-existent", updatePayload)
	assert.Equal(t, http.StatusNotFound, recUpdate404.Code)

	// List Targets (should contain the created target)
	recList2 := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets", nil)
	assert.Equal(t, http.StatusOK, recList2.Code)
	var listAfter struct {
		Targets []*models.Target `json:"targets"`
	}
	require.NoError(t, json.Unmarshal(recList2.Body.Bytes(), &listAfter))
	assert.Len(t, listAfter.Targets, 1)

	// Delete Target
	recDel := doRequest(t, rig.server, http.MethodDelete, "/api/v1/targets/tgt-unit-1", nil)
	assert.Equal(t, http.StatusOK, recDel.Code)

	// Verify Target is now 404
	recAfterDel := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/tgt-unit-1", nil)
	assert.Equal(t, http.StatusNotFound, recAfterDel.Code)

	// Delete Non-Existent Target should return 404
	recDel404 := doRequest(t, rig.server, http.MethodDelete, "/api/v1/targets/tgt-unit-1", nil)
	assert.Equal(t, http.StatusNotFound, recDel404.Code)
}

// 5. Target Status & Metrics Endpoints Tests
func TestTargetStatusAndMetrics(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	targetID := "status-metrics-target"
	tgt := models.NewTarget(targetID, models.TargetTypeHTTP, "http://127.0.0.1:8080/check", 5000, 1000)
	tgt.ID = targetID
	require.NoError(t, rig.db.CreateTarget(context.Background(), tgt))

	// Status before any probe execution should return default initial status
	recInitialStatus := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/"+targetID+"/status", nil)
	assert.Equal(t, http.StatusOK, recInitialStatus.Code)
	var initialResult models.ProbeResult
	require.NoError(t, json.Unmarshal(recInitialStatus.Body.Bytes(), &initialResult))
	assert.Equal(t, targetID, initialResult.TargetID)

	// Status for missing target should return 404
	recMissingStatus := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/missing-target/status", nil)
	assert.Equal(t, http.StatusNotFound, recMissingStatus.Code)

	// Write metric points into TSDB
	now := time.Now().UTC()
	pt1 := models.MetricPoint{
		TargetID:  targetID,
		Metric:    models.MetricProbeSuccess,
		Value:     1.0,
		Timestamp: now.Add(-2 * time.Minute),
	}
	pt2 := models.MetricPoint{
		TargetID:  targetID,
		Metric:    models.MetricProbeResponseTime,
		Value:     0.045,
		Timestamp: now.Add(-1 * time.Minute),
	}
	require.NoError(t, rig.tsdb.WritePoints([]models.MetricPoint{pt1, pt2}))

	// Query metrics without filter
	recMetrics := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/"+targetID+"/metrics", nil)
	assert.Equal(t, http.StatusOK, recMetrics.Code)
	var metricsRes struct {
		Metrics []models.MetricPoint `json:"metrics"`
	}
	require.NoError(t, json.Unmarshal(recMetrics.Body.Bytes(), &metricsRes))
	assert.Len(t, metricsRes.Metrics, 2)

	// Query metrics with start & end filter
	startStr := now.Add(-3 * time.Minute).Format(time.RFC3339)
	endStr := now.Format(time.RFC3339)
	pathWithFilter := fmt.Sprintf("/api/v1/targets/%s/metrics?start=%s&end=%s", targetID, startStr, endStr)
	recFiltered := doRequest(t, rig.server, http.MethodGet, pathWithFilter, nil)
	assert.Equal(t, http.StatusOK, recFiltered.Code)
	var filteredRes struct {
		Metrics []models.MetricPoint `json:"metrics"`
	}
	require.NoError(t, json.Unmarshal(recFiltered.Body.Bytes(), &filteredRes))
	assert.Len(t, filteredRes.Metrics, 2)

	// Query metrics for missing target should return 404
	recMissingMetrics := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets/missing/metrics", nil)
	assert.Equal(t, http.StatusNotFound, recMissingMetrics.Code)
}

// 6. Alerts & Rules Endpoints Tests
func TestAlertsAndRulesEndpoints(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	// 1. Create Alert Rule
	rulePayload := map[string]any{
		"id":           "rule-high-latency",
		"name":         "HighLatencyAlert",
		"expression":   "probe_response_time > 1.0",
		"for_duration": "30s",
		"severity":     "warning",
		"channels":     []string{"telegram"},
		"enabled":      true,
	}
	recCreateRule := doRequest(t, rig.server, http.MethodPost, "/api/v1/alerts/rules", rulePayload)
	assert.Equal(t, http.StatusCreated, recCreateRule.Code)
	var createdRule models.AlertRule
	require.NoError(t, json.Unmarshal(recCreateRule.Body.Bytes(), &createdRule))
	assert.Equal(t, "HighLatencyAlert", createdRule.Name)

	// 2. List Alert Rules
	recListRules := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts/rules", nil)
	assert.Equal(t, http.StatusOK, recListRules.Code)
	var rulesRes struct {
		Rules []*models.AlertRule `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(recListRules.Body.Bytes(), &rulesRes))
	assert.Len(t, rulesRes.Rules, 1)

	// 3. Get Single Rule
	recGetRule := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts/rules/rule-high-latency", nil)
	assert.Equal(t, http.StatusOK, recGetRule.Code)

	// 4. Update Rule
	updateRulePayload := map[string]any{
		"name":         "HighLatencyAlert-v2",
		"expression":   "probe_response_time > 2.0",
		"for_duration": "1m",
		"severity":     "critical",
		"channels":     []string{"telegram"},
		"enabled":      true,
	}
	recUpdateRule := doRequest(t, rig.server, http.MethodPut, "/api/v1/alerts/rules/rule-high-latency", updateRulePayload)
	assert.Equal(t, http.StatusOK, recUpdateRule.Code)

	// 5. Record Alert Event and Query via GET /api/v1/alerts
	event := &models.AlertEvent{
		RuleID:        "rule-high-latency",
		RuleName:      "HighLatencyAlert-v2",
		TargetName:    "prod-api",
		TargetAddress: "http://api.internal",
		Status:        models.AlertStatusFiring,
		Severity:      models.SeverityCritical,
		Value:         2.5,
		Message:       "Response time exceeded 2.0s",
		FiredAt:       time.Now().UTC(),
	}
	require.NoError(t, rig.db.RecordAlertEvent(context.Background(), event))

	recAlerts := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts", nil)
	assert.Equal(t, http.StatusOK, recAlerts.Code)
	var alertsRes struct {
		Alerts []*models.AlertEvent `json:"alerts"`
	}
	require.NoError(t, json.Unmarshal(recAlerts.Body.Bytes(), &alertsRes))
	assert.Len(t, alertsRes.Alerts, 1)
	assert.Equal(t, "HighLatencyAlert-v2", alertsRes.Alerts[0].RuleName)

	// 6. Create Alert Silence
	silencePayload := map[string]any{
		"id":         "silence-test-1",
		"matchers":   `{"target":"prod-api"}`,
		"starts_at":  time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		"ends_at":    time.Now().Add(10 * time.Minute).Format(time.RFC3339),
		"comment":    "maintenance test",
		"created_by": "qa",
	}
	recSilence := doRequest(t, rig.server, http.MethodPost, "/api/v1/alerts/silence", silencePayload)
	assert.Equal(t, http.StatusCreated, recSilence.Code)

	// 7. List Silences
	recListSil := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts/silences", nil)
	assert.Equal(t, http.StatusOK, recListSil.Code)
	var silencesRes struct {
		Silences []*models.AlertSilence `json:"silences"`
	}
	require.NoError(t, json.Unmarshal(recListSil.Body.Bytes(), &silencesRes))
	assert.Len(t, silencesRes.Silences, 1)

	// 8. Delete Silence
	recDelSil := doRequest(t, rig.server, http.MethodDelete, "/api/v1/alerts/silences/silence-test-1", nil)
	assert.Equal(t, http.StatusOK, recDelSil.Code)

	// 9. Delete Rule
	recDelRule := doRequest(t, rig.server, http.MethodDelete, "/api/v1/alerts/rules/rule-high-latency", nil)
	assert.Equal(t, http.StatusOK, recDelRule.Code)
}

// 7. Config Redaction & Reload Endpoints Tests
func TestConfigAndReloadEndpoints(t *testing.T) {
	secretPass := "UltraSecretPassword123!"
	secretJWT := "jwt-secret-very-confidential"
	botToken := "bot12345:TOKEN_CONFIDENTIAL"

	rig, cleanup := setupTestRig(t, func(c *config.Config) {
		c.Auth.AdminPassword = secretPass
		c.Auth.JWT.Secret = secretJWT
		c.Alerting.Channels = []config.AlertChannelConfig{
			{
				Name: "tg",
				Type: "telegram",
				Config: map[string]any{
					"bot_token": botToken,
					"chat_id":   "-100123",
				},
			},
		}
	})
	defer cleanup()

	// GET /api/v1/config must redact credentials with ***
	recCfg := doRequest(t, rig.server, http.MethodGet, "/api/v1/config", nil)
	assert.Equal(t, http.StatusOK, recCfg.Code)
	body := recCfg.Body.String()

	assert.NotContains(t, body, secretPass)
	assert.NotContains(t, body, secretJWT)
	assert.NotContains(t, body, botToken)
	assert.Contains(t, body, "***")

	// PUT /api/v1/config/reload triggers reload hook
	recReload := doRequest(t, rig.server, http.MethodPut, "/api/v1/config/reload", nil)
	assert.Equal(t, http.StatusOK, recReload.Code)
	assert.Equal(t, 1, rig.reloadCalls)

	// POST /api/v1/config/reload also works
	recReloadPost := doRequest(t, rig.server, http.MethodPost, "/api/v1/config/reload", nil)
	assert.Equal(t, http.StatusOK, recReloadPost.Code)
	assert.Equal(t, 2, rig.reloadCalls)
}

// 8. Authentication Middleware Tests
func TestAuthMiddleware(t *testing.T) {
	adminUser := "superadmin"
	adminPass := "supersecret99"
	apiKey := "api-key-test-token-xyz"

	rig, cleanup := setupTestRig(t, func(c *config.Config) {
		c.Auth.Enabled = true
		c.Auth.AdminUser = adminUser
		c.Auth.AdminPassword = adminPass
		c.Auth.APIKeys = []config.APIKeyConfig{
			{
				Name: "ci-service",
				Key:  apiKey,
			},
		}
	})
	defer cleanup()

	// Public paths should succeed without auth
	recHealth := doRequest(t, rig.server, http.MethodGet, "/api/v1/health", nil)
	assert.Equal(t, http.StatusOK, recHealth.Code)

	recMetrics := doRequest(t, rig.server, http.MethodGet, "/metrics", nil)
	assert.Equal(t, http.StatusOK, recMetrics.Code)

	recDash := doRequest(t, rig.server, http.MethodGet, "/", nil)
	assert.Equal(t, http.StatusOK, recDash.Code)

	// Protected endpoint (/api/v1/targets) without credentials should return 401
	recNoAuth := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets", nil)
	assert.Equal(t, http.StatusUnauthorized, recNoAuth.Code)

	// Basic Auth with invalid credentials should return 401
	recBadBasic := httptest.NewRecorder()
	reqBadBasic := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	reqBadBasic.SetBasicAuth(adminUser, "wrong-pass")
	rig.server.ServeHTTP(recBadBasic, reqBadBasic)
	assert.Equal(t, http.StatusUnauthorized, recBadBasic.Code)

	// Basic Auth with valid credentials should return 200
	recGoodBasic := httptest.NewRecorder()
	reqGoodBasic := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	reqGoodBasic.SetBasicAuth(adminUser, adminPass)
	rig.server.ServeHTTP(recGoodBasic, reqGoodBasic)
	assert.Equal(t, http.StatusOK, recGoodBasic.Code)

	// Bearer Token with admin password should return 200
	recBearerAdmin := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets", nil, "Bearer "+adminPass)
	assert.Equal(t, http.StatusOK, recBearerAdmin.Code)

	// Bearer Token with API Key should return 200
	recBearerKey := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets", nil, "Bearer "+apiKey)
	assert.Equal(t, http.StatusOK, recBearerKey.Code)

	// Bearer Token with invalid token should return 401
	recBearerBad := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets", nil, "Bearer invalid-token")
	assert.Equal(t, http.StatusUnauthorized, recBearerBad.Code)
}

// 9. CORS & Recoverer Middleware Tests
func TestCORSAndRecovererMiddleware(t *testing.T) {
	rig, cleanup := setupTestRig(t)
	defer cleanup()

	// OPTIONS preflight request
	reqOptions := httptest.NewRequest(http.MethodOptions, "/api/v1/targets", nil)
	reqOptions.Header.Set("Origin", "http://example.com")
	reqOptions.Header.Set("Access-Control-Request-Method", "POST")
	recOptions := httptest.NewRecorder()
	rig.server.ServeHTTP(recOptions, reqOptions)

	assert.Equal(t, http.StatusOK, recOptions.Code)
	assert.NotEmpty(t, recOptions.Header().Get("Access-Control-Allow-Origin"))

	// Recoverer: simulate handler panic
	rig.server.Router().Get("/panic-test", func(w http.ResponseWriter, r *http.Request) {
		panic("intentional test panic")
	})

	recPanic := doRequest(t, rig.server, http.MethodGet, "/panic-test", nil)
	assert.Equal(t, http.StatusInternalServerError, recPanic.Code)
	var panicRes map[string]string
	_ = json.Unmarshal(recPanic.Body.Bytes(), &panicRes)
	assert.Equal(t, "internal server error", panicRes["error"])
}

// 10. Server Start and Stop Lifecycle Tests
func TestServerLifecycle(t *testing.T) {
	rig, cleanup := setupTestRig(t, func(c *config.Config) {
		c.Server.Listen = "127.0.0.1:0"
	})
	defer cleanup()

	errCh := make(chan error, 1)
	go func() {
		errCh <- rig.server.Start(context.Background())
	}()

	// Wait for listener to be initialized
	var addr string
	for i := 0; i < 50; i++ {
		addr = rig.server.Addr()
		if addr != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, addr, "server listener did not initialize")

	// Verify health check over real TCP connection
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/api/v1/health", addr))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Stop server
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = rig.server.Stop(stopCtx)
	assert.NoError(t, err)

	// Wait for Start goroutine to exit
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server.Start did not terminate after Stop")
	}
}

// 11. Edge Cases and Validation Error Tests
func TestEdgeCasesAndValidation(t *testing.T) {
	jwtSecret := "test-jwt-secret-key-12345"
	rig, cleanup := setupTestRig(t, func(c *config.Config) {
		c.Auth.Enabled = true
		c.Auth.JWT.Secret = jwtSecret
	})
	defer cleanup()

	// Bearer Auth using JWT secret
	authBearer := "Bearer " + jwtSecret

	// Query targets with filters
	recFilter := doRequest(t, rig.server, http.MethodGet, "/api/v1/targets?type=http&enabled=true", nil, authBearer)
	assert.Equal(t, http.StatusOK, recFilter.Code)

	// Query alert rules with enabled filter
	recRulesFilter := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts/rules?enabled=true", nil, authBearer)
	assert.Equal(t, http.StatusOK, recRulesFilter.Code)

	// Query alerts with status=all
	recAlertsAll := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts?status=all", nil, authBearer)
	assert.Equal(t, http.StatusOK, recAlertsAll.Code)

	// Rule 404s
	recRuleGet404 := doRequest(t, rig.server, http.MethodGet, "/api/v1/alerts/rules/nonexistent", nil, authBearer)
	assert.Equal(t, http.StatusNotFound, recRuleGet404.Code)

	recRulePut404 := doRequest(t, rig.server, http.MethodPut, "/api/v1/alerts/rules/nonexistent", map[string]any{
		"name":       "Test",
		"expression": "x > 0",
		"severity":   "warning",
	}, authBearer)
	assert.Equal(t, http.StatusNotFound, recRulePut404.Code)

	recRuleDel404 := doRequest(t, rig.server, http.MethodDelete, "/api/v1/alerts/rules/nonexistent", nil, authBearer)
	assert.Equal(t, http.StatusNotFound, recRuleDel404.Code)

	// Rule validation errors (400)
	recRuleBadPost := doRequest(t, rig.server, http.MethodPost, "/api/v1/alerts/rules", map[string]any{
		"name":       "",
		"expression": "",
		"severity":   "invalid",
	}, authBearer)
	assert.Equal(t, http.StatusBadRequest, recRuleBadPost.Code)

	// Silence 404
	recSilDel404 := doRequest(t, rig.server, http.MethodDelete, "/api/v1/alerts/silences/nonexistent", nil, authBearer)
	assert.Equal(t, http.StatusNotFound, recSilDel404.Code)

	// Silence validation errors (400) - ends_at before starts_at
	recSilBadPost := doRequest(t, rig.server, http.MethodPost, "/api/v1/alerts/silence", map[string]any{
		"matchers":  `{"target":"foo"}`,
		"starts_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		"ends_at":   time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		"comment":   "inverted",
	}, authBearer)
	assert.Equal(t, http.StatusBadRequest, recSilBadPost.Code)

	// Target update with invalid address (400)
	validTgt := models.NewTarget("val-tgt", models.TargetTypeHTTP, "http://127.0.0.1:8080/valid", 5000, 1000)
	validTgt.ID = "val-tgt-1"
	require.NoError(t, rig.db.CreateTarget(context.Background(), validTgt))

	recTgtBadUpdate := doRequest(t, rig.server, http.MethodPut, "/api/v1/targets/val-tgt-1", map[string]any{
		"target": "invalid-url-without-scheme",
	}, authBearer)
	assert.Equal(t, http.StatusBadRequest, recTgtBadUpdate.Code)
}


