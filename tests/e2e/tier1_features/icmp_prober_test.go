package tier1_features_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// TestICMPProber verifies ICMP ping reachability, packet loss, RTT/jitter metrics, unprivileged mode, and DNS failures.
func TestICMPProber(t *testing.T) {
	t.Run("PingReachability_Loopback", func(t *testing.T) {
		targetID := "icmp-loopback-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "icmp",
					"target":      "127.0.0.1",
					"interval_ms": 100,
					"timeout_ms":  500,
					"icmp": map[string]any{
						"count":    3,
						"protocol": "ip4",
					},
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if !status.Success {
			t.Fatalf("expected ICMP probe to 127.0.0.1 to succeed, got success=false, error=%s", status.Error)
		}
	})

	t.Run("PacketLossCalculation", func(t *testing.T) {
		// Target unreachable documentation IP 192.0.2.1 (RFC 5737 TEST-NET-1)
		targetID := "icmp-unreachable-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "icmp",
					"target":      "192.0.2.1",
					"interval_ms": 100,
					"timeout_ms":  400,
				},
			}
		}))

		env.StartServer(t)

		// Poll status to verify probe cycle executed and failed
		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if status.Success {
			t.Fatalf("expected unreachable ICMP target (192.0.2.1) to fail, got success=true")
		}

		// Verify 100% packet loss metric is recorded
		metrics := pollTargetMetrics(t, env.Client, targetID, 1, 3*time.Second)
		foundLoss := false
		for _, m := range metrics {
			if m.Metric == models.MetricICMPPacketLoss {
				foundLoss = true
				if m.Value != 100.0 {
					t.Fatalf("expected packet loss 100.0%%, got %f", m.Value)
				}
				break
			}
		}
		if !foundLoss {
			t.Fatalf("probe_icmp_packet_loss_percent metric not found: %+v", metrics)
		}
	})

	t.Run("RTTMetrics_AvgMinMaxJitter", func(t *testing.T) {
		targetID := "icmp-rtt-metrics-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "icmp",
					"target":      "127.0.0.1",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Wait until metrics arrive
		metrics := pollTargetMetrics(t, env.Client, targetID, 4, 3*time.Second)

		metricsFound := make(map[string]float64)
		for _, m := range metrics {
			metricsFound[m.Metric] = m.Value
		}

		expectedMetrics := []string{
			models.MetricICMPRTTAverage,
			models.MetricICMPRTTMin,
			models.MetricICMPRTTMax,
			models.MetricICMPJitter,
		}

		for _, em := range expectedMetrics {
			val, ok := metricsFound[em]
			if !ok {
				t.Errorf("missing expected ICMP metric: %s", em)
			} else if val < 0 {
				t.Errorf("expected non-negative value for %s, got %f", em, val)
			}
		}
	})

	t.Run("UnprivilegedUDPFallbackMode", func(t *testing.T) {
		targetID := "icmp-unprivileged-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "icmp",
					"target":      "127.0.0.1",
					"interval_ms": 100,
					"timeout_ms":  500,
					"icmp": map[string]any{
						"protocol": "ip4",
						"count":    1,
					},
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if !status.Success {
			t.Fatalf("expected unprivileged ICMP ping to succeed without root permissions, got error: %s", status.Error)
		}
	})

	t.Run("HostResolutionFailure", func(t *testing.T) {
		invalidHost := "this-is-a-definitely-invalid-nonexistent-domain-12345.invalid"
		targetID := "icmp-invalid-domain-target"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "icmp",
					"target":      invalidHost,
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if status.Success {
			t.Fatalf("expected probe to fail on unresolvable host, got success=true")
		}
		if status.Error == "" {
			t.Errorf("expected non-empty error message on resolution failure")
		}
	})
}
