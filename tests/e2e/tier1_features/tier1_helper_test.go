package tier1_features_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// requireBinary ensures the monstack binary exists or skips/fails according to Dual Track mode.
func requireBinary(t testing.TB) string {
	t.Helper()
	return harness.RequireBinary(t)
}

// setupTier1Test connects directly to harness.NewTestEnv(t) with the verified binary.
func setupTier1Test(t testing.TB, opts ...harness.EnvOption) *harness.TestEnv {
	t.Helper()
	binPath := requireBinary(t)
	allOpts := append([]harness.EnvOption{harness.WithBinaryPath(binPath)}, opts...)
	return harness.NewTestEnv(t, allOpts...)
}

// newTier1TestEnv is an alias for setupTier1Test across existing test cases.
func newTier1TestEnv(t testing.TB, opts ...harness.EnvOption) *harness.TestEnv {
	t.Helper()
	return setupTier1Test(t, opts...)
}

// resolveBinary returns the verified path to the monstack binary.
func resolveBinary(t testing.TB) string {
	t.Helper()
	return requireBinary(t)
}

// pollForCondition polls cond every interval until it returns true or timeout expires.
func pollForCondition(t testing.TB, timeout, interval time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(interval)
	}
	if !cond() {
		t.Fatalf("timed out after %v waiting for condition", timeout)
	}
}

// pollTargetStatus polls /api/v1/targets/{id}/status until valid probe execution or timeout.
func pollTargetStatus(t testing.TB, client *harness.Client, targetID string, timeout time.Duration) *models.ProbeResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	path := fmt.Sprintf("/api/v1/targets/%s/status", targetID)
	for time.Now().Before(deadline) {
		resp, err := client.GetRaw(path)
		if err == nil && resp.StatusCode == http.StatusOK {
			var result models.ProbeResult
			if err := resp.JSON(&result); err == nil && !result.Timestamp.IsZero() {
				return &result
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for probe status for target %s after %v", targetID, timeout)
	return nil
}

// pollTargetMetrics polls /api/v1/targets/{id}/metrics until minCount points arrive or timeout.
func pollTargetMetrics(t testing.TB, client *harness.Client, targetID string, minCount int, timeout time.Duration) []models.MetricPoint {
	t.Helper()
	return pollTargetMetricsWithFilter(t, client, targetID, "", minCount, timeout)
}

// pollTargetMetricsWithFilter polls /api/v1/targets/{id}/metrics with query params until minCount points arrive or timeout.
func pollTargetMetricsWithFilter(t testing.TB, client *harness.Client, targetID, queryParams string, minCount int, timeout time.Duration) []models.MetricPoint {
	t.Helper()
	deadline := time.Now().Add(timeout)
	path := fmt.Sprintf("/api/v1/targets/%s/metrics", targetID)
	if queryParams != "" {
		path = fmt.Sprintf("%s?%s", path, queryParams)
	}
	for time.Now().Before(deadline) {
		resp, err := client.GetRaw(path)
		if err == nil && resp.StatusCode == http.StatusOK {
			var res struct {
				Metrics []models.MetricPoint `json:"metrics"`
			}
			if err := resp.JSON(&res); err == nil && len(res.Metrics) >= minCount {
				return res.Metrics
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d metrics at %s after %v", minCount, path, timeout)
	return nil
}

// pollForActiveAlerts polls /api/v1/alerts until minCount alerts are present or timeout.
func pollForActiveAlerts(t testing.TB, client *harness.Client, minCount int, timeout time.Duration) []models.AlertEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.GetRaw("/api/v1/alerts")
		if err == nil && resp.StatusCode == http.StatusOK {
			var res struct {
				Alerts []models.AlertEvent `json:"alerts"`
			}
			if err := resp.JSON(&res); err == nil && len(res.Alerts) >= minCount {
				return res.Alerts
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d active alerts after %v", minCount, timeout)
	return nil
}
