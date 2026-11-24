package tier1_features_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// TestAPIAndWebDashboard tests the Chi REST API endpoints and embedded web dashboard.
func TestAPIAndWebDashboard(t *testing.T) {
	t.Run("HealthEndpoint", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		resp := env.Client.Get(t, "/api/v1/health")
		resp.AssertOK(t)

		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		var health struct {
			Status        string         `json:"status"`
			Version       string         `json:"version"`
			Uptime        string         `json:"uptime"`
			UptimeSeconds float64        `json:"uptime_seconds"`
			Subsystems    map[string]any `json:"subsystems"`
		}
		if err := resp.JSON(&health); err != nil {
			t.Fatalf("failed to decode health JSON: %v", err)
		}
		if health.Status != "up" {
			t.Fatalf("expected health status 'up', got %s", health.Status)
		}
		if len(health.Subsystems) == 0 {
			t.Errorf("expected non-empty subsystems health details")
		}
	})

	t.Run("TargetsCRUDEndpoints", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// 1. POST /api/v1/targets
		newTarget := map[string]any{
			"id":          "api-crud-target-1",
			"name":        "api-target-alpha",
			"type":        "http",
			"target":      "http://127.0.0.1:8080/api-test",
			"interval_ms": 5000,
			"timeout_ms":  2000,
			"enabled":     true,
			"labels": map[string]string{
				"cluster": "us-east-1",
			},
		}
		postResp := env.Client.PostJSON(t, "/api/v1/targets", newTarget)
		if postResp.StatusCode != http.StatusCreated && postResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 201 or 200 on create, got %d. Body: %s", postResp.StatusCode, postResp.String())
		}

		// 2. GET /api/v1/targets
		listResp := env.Client.Get(t, "/api/v1/targets")
		listResp.AssertOK(t)
		var listResult struct {
			Targets []*models.Target `json:"targets"`
		}
		if err := listResp.JSON(&listResult); err != nil {
			t.Fatalf("failed to parse targets list: %v", err)
		}
		found := false
		for _, tgt := range listResult.Targets {
			if tgt.ID == "api-crud-target-1" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("target 'api-crud-target-1' not found in targets list")
		}

		// 3. GET /api/v1/targets/{id}
		getResp := env.Client.Get(t, "/api/v1/targets/api-crud-target-1")
		getResp.AssertOK(t)
		var singleTarget models.Target
		if err := getResp.JSON(&singleTarget); err != nil {
			t.Fatalf("failed to parse single target: %v", err)
		}
		if singleTarget.Name != "api-target-alpha" {
			t.Errorf("expected name 'api-target-alpha', got %s", singleTarget.Name)
		}

		// 4. PUT /api/v1/targets/{id}
		putResp := env.Client.PutJSON(t, "/api/v1/targets/api-crud-target-1", map[string]any{
			"name":        "api-target-alpha-v2",
			"interval_ms": 8000,
			"enabled":     true,
		})
		putResp.AssertOK(t)

		getUpdated := env.Client.Get(t, "/api/v1/targets/api-crud-target-1")
		getUpdated.AssertOK(t)
		var updated models.Target
		_ = getUpdated.JSON(&updated)
		if updated.Name != "api-target-alpha-v2" {
			t.Errorf("expected updated name 'api-target-alpha-v2', got %s", updated.Name)
		}
		if updated.IntervalMs != 8000 {
			t.Errorf("expected interval 8000, got %d", updated.IntervalMs)
		}

		// 5. DELETE /api/v1/targets/{id}
		delResp := env.Client.Delete(t, "/api/v1/targets/api-crud-target-1")
		delResp.AssertOK(t)

		// Verify 404 after deletion
		notFoundResp, _ := env.Client.GetRaw("/api/v1/targets/api-crud-target-1")
		if notFoundResp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found after target deletion, got %d", notFoundResp.StatusCode)
		}
	})

	t.Run("TargetStatusEndpoint", func(t *testing.T) {
		targetID := "status-endpoint-target"
		env := newTier1TestEnv(t)

		env.StartServer(t)

		// Create target
		env.Client.PostJSON(t, "/api/v1/targets", map[string]any{
			"id":          targetID,
			"name":        targetID,
			"type":        "http",
			"target":      "http://127.0.0.1:8080/probe-status-check",
			"interval_ms": 5000,
			"timeout_ms":  2000,
			"enabled":     true,
		})

		// Query status endpoint
		statusResp := env.Client.Get(t, fmt.Sprintf("/api/v1/targets/%s/status", targetID))
		statusResp.AssertOK(t)

		var status models.ProbeResult
		if err := statusResp.JSON(&status); err != nil {
			t.Fatalf("failed to parse probe status: %v", err)
		}
		if status.TargetID != targetID {
			t.Errorf("expected target ID %q, got %q", targetID, status.TargetID)
		}
	})

	t.Run("AlertsAndRulesEndpoints", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// 1. GET /api/v1/alerts
		alertsResp := env.Client.Get(t, "/api/v1/alerts")
		alertsResp.AssertOK(t)
		var alertsResult struct {
			Alerts []models.AlertEvent `json:"alerts"`
		}
		if err := alertsResp.JSON(&alertsResult); err != nil {
			t.Fatalf("failed to decode alerts: %v", err)
		}

		// 2. GET /api/v1/alerts/rules
		rulesResp := env.Client.Get(t, "/api/v1/alerts/rules")
		rulesResp.AssertOK(t)
		var rulesResult struct {
			Rules []*models.AlertRule `json:"rules"`
		}
		if err := rulesResp.JSON(&rulesResult); err != nil {
			t.Fatalf("failed to decode rules: %v", err)
		}
	})

	t.Run("ConfigViewAndReloadEndpoints", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// 1. GET /api/v1/config
		cfgResp := env.Client.Get(t, "/api/v1/config")
		cfgResp.AssertOK(t)
		if ct := cfgResp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("expected application/json for config endpoint, got %s", ct)
		}

		// 2. PUT /api/v1/config/reload
		reloadResp := env.Client.PutJSON(t, "/api/v1/config/reload", nil)
		reloadResp.AssertOK(t)
	})

	t.Run("PrometheusMetricsExposition", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		resp := env.Client.Get(t, "/metrics")
		resp.AssertOK(t)

		body := resp.String()
		if !strings.Contains(body, "# HELP") || !strings.Contains(body, "# TYPE") {
			t.Fatalf("expected Prometheus format with # HELP and # TYPE, got:\n%s", body)
		}
		if !strings.Contains(body, "monstack_") {
			t.Fatalf("expected metric namespace 'monstack_' in /metrics, got:\n%s", body)
		}
	})

	t.Run("WebDashboard_HTMLServing", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		resp := env.Client.Get(t, "/")
		resp.AssertOK(t)

		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Errorf("expected Content-Type text/html for dashboard, got %s", ct)
		}

		body := resp.String()
		if !strings.Contains(body, "<!DOCTYPE html>") && !strings.Contains(body, "<html") {
			t.Fatalf("expected HTML document for dashboard root, got:\n%s", body)
		}
		if !strings.Contains(strings.ToLower(body), "monstack") {
			t.Fatalf("expected 'MonStack' in dashboard title or body, got:\n%s", body)
		}
	})
}
