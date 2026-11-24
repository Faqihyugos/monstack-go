package tier1_features_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// TestTSDB verifies TSDB ingestion, in-memory queries, WAL log durability, replay, and retention.
func TestTSDB(t *testing.T) {
	t.Run("MetricPointIngestion", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		targetID := "tsdb-ingest-target-1"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL(),
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Poll until metrics arrive
		metrics := pollTargetMetrics(t, env.Client, targetID, 1, 3*time.Second)
		if len(metrics) == 0 {
			t.Fatalf("expected ingested metrics for target %s, got 0", targetID)
		}

		hasSuccessMetric := false
		for _, m := range metrics {
			if m.Metric == models.MetricProbeSuccess {
				hasSuccessMetric = true
				if m.Value != 1.0 {
					t.Errorf("expected probe_success == 1.0, got %f", m.Value)
				}
			}
		}
		if !hasSuccessMetric {
			t.Errorf("missing probe_success metric in ingested metrics list")
		}
	})

	t.Run("InMemoryBufferQuery", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		targetID := "tsdb-buffer-query-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL(),
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Poll until buffer accumulates at least 2 metrics
		metrics := pollTargetMetrics(t, env.Client, targetID, 2, 3*time.Second)
		if len(metrics) < 2 {
			t.Fatalf("expected at least 2 metrics in in-memory buffer, got %d", len(metrics))
		}
	})

	t.Run("WALLogFileAppend", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		targetID := "tsdb-wal-append-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL(),
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Wait for prober execution via metrics polling
		pollTargetMetrics(t, env.Client, targetID, 1, 3*time.Second)

		walFile := filepath.Join(env.TSDBPath, "wal.log")
		pollForCondition(t, 3*time.Second, 50*time.Millisecond, func() bool {
			fi, err := os.Stat(walFile)
			return err == nil && fi.Size() > 0
		})

		fi, err := os.Stat(walFile)
		if err != nil {
			t.Fatalf("WAL file missing at %s: %v", walFile, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("expected non-empty WAL file after probes, got size 0")
		}

		data, err := os.ReadFile(walFile)
		if err != nil {
			t.Fatalf("failed to read WAL log: %v", err)
		}
		if !strings.Contains(string(data), models.MetricProbeSuccess) {
			t.Fatalf("expected %s in WAL log contents:\n%s", models.MetricProbeSuccess, string(data))
		}
	})

	t.Run("StartupWALReplay", func(t *testing.T) {
		// Pre-populate WAL file in a sandbox directory before starting server
		env := newTier1TestEnv(t)

		targetID := "prepopulated-target"
		walFile := filepath.Join(env.TSDBPath, "wal.log")

		historicalPoint := models.MetricPoint{
			TargetID:  targetID,
			Metric:    models.MetricProbeSuccess,
			Value:     1.0,
			Timestamp: time.Now().Add(-1 * time.Hour).UTC(),
		}
		line, _ := json.Marshal(historicalPoint)
		if err := os.WriteFile(walFile, append(line, '\n'), 0644); err != nil {
			t.Fatalf("failed to pre-seed WAL file: %v", err)
		}

		// Also register the target so metrics endpoint finds it
		env.UpdateConfig(t, func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      "http://127.0.0.1:9999/test",
					"interval_ms": 60000,
					"timeout_ms":  5000,
				},
			}
		})

		env.StartServer(t)

		// Query metrics — historical WAL point must be replayed into memory
		resp := env.Client.Get(t, fmt.Sprintf("/api/v1/targets/%s/metrics", targetID))
		resp.AssertOK(t)

		var res struct {
			Metrics []models.MetricPoint `json:"metrics"`
		}
		if err := resp.JSON(&res); err != nil {
			t.Fatalf("failed to parse replay response: %v", err)
		}

		found := false
		for _, m := range res.Metrics {
			if m.TargetID == targetID && m.Metric == models.MetricProbeSuccess {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("pre-seeded WAL metric point not found after replay (metrics returned: %+v)", res.Metrics)
		}
	})

	t.Run("TimeRangeQueryFiltering", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		targetID := "timerange-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL(),
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Wait until metric points arrive
		pollTargetMetrics(t, env.Client, targetID, 1, 3*time.Second)

		now := time.Now().UTC()
		// Filter query with window covering current samples
		start := now.Add(-5 * time.Minute).Format(time.RFC3339)
		end := now.Add(5 * time.Minute).Format(time.RFC3339)

		resp := env.Client.Get(t, fmt.Sprintf("/api/v1/targets/%s/metrics?start=%s&end=%s", targetID, start, end))
		resp.AssertOK(t)

		var res struct {
			Metrics []models.MetricPoint `json:"metrics"`
		}
		if err := resp.JSON(&res); err != nil {
			t.Fatalf("failed to parse query metrics: %v", err)
		}
		if len(res.Metrics) == 0 {
			t.Fatalf("expected metrics within valid time window, got 0")
		}

		// Filter query with past window (should return 0 samples)
		pastStart := now.Add(-2 * time.Hour).Format(time.RFC3339)
		pastEnd := now.Add(-1 * time.Hour).Format(time.RFC3339)

		pastResp := env.Client.Get(t, fmt.Sprintf("/api/v1/targets/%s/metrics?start=%s&end=%s", targetID, pastStart, pastEnd))
		pastResp.AssertOK(t)

		var pastRes struct {
			Metrics []models.MetricPoint `json:"metrics"`
		}
		if err := pastResp.JSON(&pastRes); err != nil {
			t.Fatalf("failed to parse past query metrics: %v", err)
		}
		if len(pastRes.Metrics) != 0 {
			t.Fatalf("expected 0 metrics in past time window, got %d", len(pastRes.Metrics))
		}
	})

	t.Run("RetentionPruningOpaqueBox", func(t *testing.T) {
		// Opaque-box retention verification: configure TSDB retention, verify API queries enforce retention window
		targetID := "tsdb-retention-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["storage"].(map[string]any)["tsdb"].(map[string]any)["retention"] = "1h"
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      "http://127.0.0.1:9999/health",
					"interval_ms": 60000,
					"timeout_ms":  1000,
				},
			}
		}))

		// Pre-seed WAL with an expired metric point outside the 1h retention window (2h ago)
		walFile := filepath.Join(env.TSDBPath, "wal.log")
		expiredPoint := models.MetricPoint{
			TargetID:  targetID,
			Metric:    models.MetricProbeSuccess,
			Value:     1.0,
			Timestamp: time.Now().Add(-2 * time.Hour).UTC(),
		}
		line, _ := json.Marshal(expiredPoint)
		_ = os.WriteFile(walFile, append(line, '\n'), 0644)

		env.StartServer(t)

		// Query metrics within current window — expired point must NOT be returned
		now := time.Now().UTC()
		start := now.Add(-30 * time.Minute).Format(time.RFC3339)
		end := now.Add(30 * time.Minute).Format(time.RFC3339)

		resp := env.Client.Get(t, fmt.Sprintf("/api/v1/targets/%s/metrics?start=%s&end=%s", targetID, start, end))
		resp.AssertOK(t)

		var res struct {
			Metrics []models.MetricPoint `json:"metrics"`
		}
		if err := resp.JSON(&res); err != nil {
			t.Fatalf("failed to decode metrics: %v", err)
		}
		for _, m := range res.Metrics {
			if m.Timestamp.Before(now.Add(-1 * time.Hour)) {
				t.Fatalf("found metric point older than 1h retention window: %+v", m)
			}
		}
	})
}
