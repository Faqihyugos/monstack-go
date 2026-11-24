package tier1_features_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// TestHTTPProber verifies 200 OK, non-200 failures, latency measurement, SSL expiry, content matching, and redirects.
func TestHTTPProber(t *testing.T) {
	t.Run("Status200OK_Success", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		mockHTTP.AddStatusRoute("/healthz", http.StatusOK, `{"status":"healthy"}`)

		targetID := "http-200-ok-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/healthz",
					"interval_ms": 100,
					"timeout_ms":  500,
					"http": map[string]any{
						"method":          "GET",
						"expected_status": []int{200},
					},
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if !status.Success {
			t.Fatalf("expected probe success=true for 200 OK endpoint, got success=false, error=%s", status.Error)
		}
		if status.ResponseTime <= 0 {
			t.Fatalf("expected non-zero response time, got %v", status.ResponseTime)
		}
	})

	t.Run("Non200Failure_Detection", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		mockHTTP.AddStatusRoute("/internal-error", http.StatusInternalServerError, "Database down")

		targetID := "http-500-fail-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/internal-error",
					"interval_ms": 100,
					"timeout_ms":  500,
					"http": map[string]any{
						"expected_status": []int{200},
					},
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if status.Success {
			t.Fatalf("expected probe success=false for 500 error endpoint, got success=true")
		}
	})

	t.Run("LatencyMeasurement", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		injectedDelay := 100 * time.Millisecond
		mockHTTP.SetDelay(injectedDelay)

		targetID := "http-latency-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/delayed",
					"interval_ms": 100,
					"timeout_ms":  2000,
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if status.ResponseTime < injectedDelay {
			t.Fatalf("expected response time >= %v, got %v", injectedDelay, status.ResponseTime)
		}
	})

	t.Run("SSLCertificateExpiry_RemainingDays", func(t *testing.T) {
		validDays := 45 * 24 * time.Hour
		mockHTTPS, err := harness.NewMockHTTPSServer(harness.TLSCertOptions{
			CommonName: "127.0.0.1",
			ValidFor:   validDays,
		})
		if err != nil {
			t.Fatalf("failed to create HTTPS mock server: %v", err)
		}
		defer mockHTTPS.Close()

		targetID := "https-ssl-expiry-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTPS.URL() + "/cert-check",
					"interval_ms": 100,
					"timeout_ms":  1000,
				},
			}
		}))

		env.StartServer(t)

		// Query metrics to inspect SSL days remaining metric
		metrics := pollTargetMetrics(t, env.Client, targetID, 1, 3*time.Second)

		foundSSLMetric := false
		for _, m := range metrics {
			if m.Metric == models.MetricHTTPSSLExpiryDays {
				foundSSLMetric = true
				if m.Value < 40 || m.Value > 46 {
					t.Fatalf("expected ~45 days remaining, got %f", m.Value)
				}
				break
			}
		}
		if !foundSSLMetric {
			t.Fatalf("probe_ssl_days_remaining metric not found in probe metrics: %+v", metrics)
		}
	})

	t.Run("BodyContentPatternMatch", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		mockHTTP.AddContentMatchRoute("/version-info", `{"app":"monstack","version":"1.0.0","ready":true}`)

		// 1. Target with matching pattern -> SUCCESS
		successTargetID := "http-content-match-success"
		// 2. Target with mismatched pattern -> FAILURE
		failTargetID := "http-content-match-failure"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        successTargetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/version-info",
					"interval_ms": 100,
					"timeout_ms":  500,
					"http": map[string]any{
						"body_match": `"ready":true`,
					},
				},
				map[string]any{
					"name":        failTargetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/version-info",
					"interval_ms": 100,
					"timeout_ms":  500,
					"http": map[string]any{
						"body_match": `"ready":false`,
					},
				},
			}
		}))

		env.StartServer(t)

		// Success target
		st1 := pollTargetStatus(t, env.Client, successTargetID, 3*time.Second)
		if !st1.Success {
			t.Fatalf("expected content match to succeed for target %s, got false", successTargetID)
		}

		// Failure target
		st2 := pollTargetStatus(t, env.Client, failTargetID, 3*time.Second)
		if st2.Success {
			t.Fatalf("expected content match to fail for target %s, got true", failTargetID)
		}
	})

	t.Run("RedirectFollowing", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		mockHTTP.AddRedirectChain("/redirect-start", 2, "/final-dest", "DestinationReached")

		targetID := "http-redirect-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/redirect-start",
					"interval_ms": 100,
					"timeout_ms":  1000,
					"http": map[string]any{
						"follow_redirects": true,
						"max_redirects":    5,
					},
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if !status.Success {
			t.Fatalf("expected successful redirect follow to final destination, got success=false")
		}
	})
}
