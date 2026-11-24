package tier1_features_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// TestTCPProber verifies port reachability, connect latency, banners, expect matching, and closed ports.
func TestTCPProber(t *testing.T) {
	t.Run("PortReachability_OpenPort", func(t *testing.T) {
		mockTCP, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to start MockTCPServer: %v", err)
		}
		defer mockTCP.Close()

		targetID := "tcp-open-port-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "tcp",
					"target":      mockTCP.Addr(),
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if !status.Success {
			t.Fatalf("expected open TCP port probe to succeed, got success=false, error=%s", status.Error)
		}
	})

	t.Run("ClosedPortRejection", func(t *testing.T) {
		freePort, err := harness.GetFreePort()
		if err != nil {
			t.Fatalf("failed to get free port: %v", err)
		}

		closedAddr := fmt.Sprintf("127.0.0.1:%d", freePort)
		targetID := "tcp-closed-port-target"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "tcp",
					"target":      closedAddr,
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if status.Success {
			t.Fatalf("expected closed TCP port to report success=false, got success=true")
		}
		if status.Error == "" {
			t.Errorf("expected non-empty connection error for closed port")
		}
	})

	t.Run("ConnectLatencyMeasurement", func(t *testing.T) {
		mockTCP, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to start MockTCPServer: %v", err)
		}
		defer mockTCP.Close()

		targetID := "tcp-connect-latency-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "tcp",
					"target":      mockTCP.Addr(),
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		metrics := pollTargetMetrics(t, env.Client, targetID, 1, 3*time.Second)
		foundLatency := false
		for _, m := range metrics {
			if m.Metric == models.MetricTCPConnectSeconds {
				foundLatency = true
				if m.Value <= 0 {
					t.Fatalf("expected connect latency > 0, got %f", m.Value)
				}
				break
			}
		}
		if !foundLatency {
			t.Fatalf("probe_tcp_connect_time_seconds metric not found in metrics: %+v", metrics)
		}
	})

	t.Run("BannerGreetingMatch", func(t *testing.T) {
		mockTCP, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to create MockTCPServer: %v", err)
		}
		defer mockTCP.Close()

		mockTCP.SetBanner("SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.6\r\n")

		// 1. Success target expects "SSH-2.0"
		successTargetID := "tcp-banner-match-success"
		// 2. Failure target expects "MySQL"
		failTargetID := "tcp-banner-match-fail"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        successTargetID,
					"type":        "tcp",
					"target":      mockTCP.Addr(),
					"interval_ms": 100,
					"timeout_ms":  500,
					"tcp": map[string]any{
						"expect": "SSH-2.0",
					},
				},
				map[string]any{
					"name":        failTargetID,
					"type":        "tcp",
					"target":      mockTCP.Addr(),
					"interval_ms": 100,
					"timeout_ms":  500,
					"tcp": map[string]any{
						"expect": "MySQL-Protocol-5.7",
					},
				},
			}
		}))

		env.StartServer(t)

		// Success check
		st1 := pollTargetStatus(t, env.Client, successTargetID, 3*time.Second)
		if !st1.Success {
			t.Fatalf("expected banner match success for target %s, got false", successTargetID)
		}

		// Fail check
		st2 := pollTargetStatus(t, env.Client, failTargetID, 3*time.Second)
		if st2.Success {
			t.Fatalf("expected banner match failure for target %s, got true", failTargetID)
		}
	})

	t.Run("ExpectResponsePayloadValidation", func(t *testing.T) {
		mockTCP, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to create MockTCPServer: %v", err)
		}
		defer mockTCP.Close()

		mockTCP.SetExpectMatch("PING\r\n", "PONG\r\n")

		targetID := "tcp-expect-response-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "tcp",
					"target":      mockTCP.Addr(),
					"interval_ms": 100,
					"timeout_ms":  500,
					"tcp": map[string]any{
						"send":   "PING\r\n",
						"expect": "PONG",
					},
				},
			}
		}))

		env.StartServer(t)

		status := pollTargetStatus(t, env.Client, targetID, 3*time.Second)
		if !status.Success {
			t.Fatalf("expected PING/PONG exchange to succeed, got success=false, error=%s", status.Error)
		}
	})
}
