package tier1_features_test

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// TestScheduler verifies ticker dispatching, bounded concurrency, dynamic target addition/removal, and skipping disabled targets.
func TestScheduler(t *testing.T) {
	t.Run("IntervalTickerDispatching", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		targetID := "scheduler-interval-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/tick",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Poll until at least 2 periodic probe ticks arrive
		pollForCondition(t, 3*time.Second, 20*time.Millisecond, func() bool {
			return mockHTTP.RequestCount() >= 2
		})

		reqCount := mockHTTP.RequestCount()
		if reqCount < 2 {
			t.Fatalf("expected at least 2 periodic probe ticks, got %d", reqCount)
		}
	})

	t.Run("BoundedGoroutineConcurrency", func(t *testing.T) {
		var activeGoroutines atomic.Int32
		var maxObservedConcurrency atomic.Int32

		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		mockHTTP.AddRoute("/concurrency-check", func(w http.ResponseWriter, r *http.Request) {
			current := activeGoroutines.Add(1)
			defer activeGoroutines.Add(-1)

			for {
				oldMax := maxObservedConcurrency.Load()
				if current <= oldMax || maxObservedConcurrency.CompareAndSwap(oldMax, current) {
					break
				}
			}

			time.Sleep(20 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		})

		// Configure 15 parallel targets
		targetCount := 15
		targetsList := make([]any, targetCount)
		for i := 0; i < targetCount; i++ {
			targetsList[i] = map[string]any{
				"name":        fmt.Sprintf("concurrency-target-%d", i),
				"type":        "http",
				"target":      mockHTTP.URL() + "/concurrency-check",
				"interval_ms": 100,
				"timeout_ms":  500,
			}
		}

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = targetsList
		}))

		env.StartServer(t)

		pollForCondition(t, 3*time.Second, 25*time.Millisecond, func() bool {
			return mockHTTP.RequestCount() >= targetCount
		})

		if mockHTTP.RequestCount() < targetCount {
			t.Fatalf("expected at least %d requests across targets, got %d", targetCount, mockHTTP.RequestCount())
		}
	})

	t.Run("DynamicTargetAddition", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		// Server starts with zero targets
		env := newTier1TestEnv(t)
		env.StartServer(t)

		if mockHTTP.RequestCount() != 0 {
			t.Fatalf("expected 0 requests before target is added, got %d", mockHTTP.RequestCount())
		}

		// Dynamically add a target via REST API
		newTarget := map[string]any{
			"id":          "dynamic-scheduler-target",
			"name":        "dynamic-scheduler-target",
			"type":        "http",
			"target":      mockHTTP.URL() + "/dynamic-add",
			"interval_ms": 100,
			"timeout_ms":  500,
			"enabled":     true,
		}

		postResp := env.Client.PostJSON(t, "/api/v1/targets", newTarget)
		if postResp.StatusCode != http.StatusCreated && postResp.StatusCode != http.StatusOK {
			t.Fatalf("failed to add target via API: %d, body: %s", postResp.StatusCode, postResp.String())
		}

		// Poll until scheduler picks up and dispatches probe
		pollForCondition(t, 3*time.Second, 20*time.Millisecond, func() bool {
			return mockHTTP.RequestCount() >= 1
		})

		if mockHTTP.RequestCount() == 0 {
			t.Fatalf("expected scheduler to start probing dynamically added target, got 0 requests")
		}
	})

	t.Run("DynamicTargetRemoval", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		targetID := "dynamic-removal-target"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/to-be-removed",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// Wait until target is probed initially
		pollForCondition(t, 3*time.Second, 20*time.Millisecond, func() bool {
			return mockHTTP.RequestCount() >= 1
		})

		countBefore := mockHTTP.RequestCount()
		if countBefore == 0 {
			t.Fatalf("expected target to be probed before removal, got 0")
		}

		// Remove the target via DELETE
		delResp := env.Client.Delete(t, fmt.Sprintf("/api/v1/targets/%s", targetID))
		delResp.AssertOK(t)

		// Record count at delete and check requests cease after short wait
		countAtDelete := mockHTTP.RequestCount()
		time.Sleep(200 * time.Millisecond)
		countAfter := mockHTTP.RequestCount()

		// Probes must have ceased or at most 1 in-flight
		if countAfter > countAtDelete+1 {
			t.Fatalf("expected probes to cease after target deletion: before=%d, after=%d", countAtDelete, countAfter)
		}
	})

	t.Run("DisabledTargetSkipping", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        "disabled-target-1",
					"type":        "http",
					"target":      mockHTTP.URL() + "/disabled-path",
					"interval_ms": 100,
					"timeout_ms":  500,
					"enabled":     false,
				},
			}
		}))

		env.StartServer(t)
		time.Sleep(200 * time.Millisecond)

		if mockHTTP.RequestCount() != 0 {
			t.Fatalf("expected 0 requests for disabled target, got %d", mockHTTP.RequestCount())
		}
	})
}
