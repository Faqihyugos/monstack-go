package tier1_features_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
	"github.com/Faqihyugos/monstack-go/tests/e2e/harness"
)

// TestAlerting verifies threshold evaluation, 'for' duration, transitions, silences, and Telegram dispatches.
func TestAlerting(t *testing.T) {
	t.Run("ThresholdEvaluation", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		// Endpoint returns 500 -> probe_success == 0
		mockHTTP.AddStatusRoute("/failing-endpoint", http.StatusInternalServerError)

		targetID := "alert-threshold-target"
		ruleID := "alert-down-rule"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/failing-endpoint",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
			cfg["alerting"].(map[string]any)["rules"] = []any{
				map[string]any{
					"name":     ruleID,
					"expr":     "probe_success == 0",
					"for":      "0s",
					"severity": "critical",
				},
			}
		}))

		env.StartServer(t)

		// Poll active alerts
		alerts := pollForActiveAlerts(t, env.Client, 1, 3*time.Second)
		if len(alerts) == 0 {
			t.Fatalf("expected active alert for failing target, got 0 alerts")
		}
		if alerts[0].RuleName != ruleID {
			t.Errorf("expected rule %q, got %q", ruleID, alerts[0].RuleName)
		}
		if alerts[0].Status != models.AlertStatusFiring {
			t.Errorf("expected status 'firing', got %q", alerts[0].Status)
		}
	})

	t.Run("PendingDurationForEnforcement", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()
		mockHTTP.AddStatusRoute("/flaky", http.StatusBadGateway)

		targetID := "alert-pending-for-target"
		ruleID := "rule-with-for-duration"

		// Rule requires condition to persist for 200ms before firing
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/flaky",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
			cfg["alerting"].(map[string]any)["rules"] = []any{
				map[string]any{
					"name":     ruleID,
					"expr":     "probe_success == 0",
					"for":      "200ms",
					"severity": "warning",
				},
			}
		}))

		env.StartServer(t)

		// Initially, before 'for' duration elapses, firing alerts should be 0
		respEarly := env.Client.Get(t, "/api/v1/alerts")
		respEarly.AssertOK(t)
		var resEarly struct {
			Alerts []models.AlertEvent `json:"alerts"`
		}
		_ = respEarly.JSON(&resEarly)
		if len(resEarly.Alerts) != 0 {
			t.Fatalf("expected 0 FIRING alerts immediately before 'for' duration elapsed, got %d", len(resEarly.Alerts))
		}

		// Poll until 'for' duration elapses and alert transition to FIRING occurs
		alerts := pollForActiveAlerts(t, env.Client, 1, 3*time.Second)
		if len(alerts) == 0 {
			t.Fatalf("expected alert to be FIRING after 'for' duration elapsed, got 0 alerts")
		}
	})

	t.Run("AlertStateTransitions_PendingToFiringToResolved", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()

		// Start with failing endpoint
		mockHTTP.SetDefaultResponse(http.StatusInternalServerError, "Down")

		targetID := "lifecycle-alert-target"
		ruleID := "lifecycle-rule"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/status",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
			cfg["alerting"].(map[string]any)["rules"] = []any{
				map[string]any{
					"name":     ruleID,
					"expr":     "probe_success == 0",
					"for":      "0s",
					"severity": "critical",
				},
			}
		}))

		env.StartServer(t)

		// 1. Initial failure -> FIRING
		alerts := pollForActiveAlerts(t, env.Client, 1, 3*time.Second)
		if len(alerts) == 0 {
			t.Fatalf("expected firing alert on initial failure")
		}

		// 2. Recovery: mock server recovers to 200 OK
		mockHTTP.SetDefaultResponse(http.StatusOK, "Healthy")

		// 3. Poll active alerts until empty (RESOLVED)
		pollForCondition(t, 3*time.Second, 50*time.Millisecond, func() bool {
			resp, err := env.Client.GetRaw("/api/v1/alerts")
			if err == nil && resp.StatusCode == http.StatusOK {
				var res struct {
					Alerts []models.AlertEvent `json:"alerts"`
				}
				if err := resp.JSON(&res); err == nil {
					return len(res.Alerts) == 0
				}
			}
			return false
		})

		resp2 := env.Client.Get(t, "/api/v1/alerts")
		resp2.AssertOK(t)
		var res2 struct {
			Alerts []models.AlertEvent `json:"alerts"`
		}
		_ = resp2.JSON(&res2)
		if len(res2.Alerts) != 0 {
			t.Fatalf("expected 0 active firing alerts after service recovered, got %d", len(res2.Alerts))
		}
	})

	t.Run("SilenceSuppression", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()
		mockHTTP.SetDefaultResponse(http.StatusServiceUnavailable, "Under Maintenance")

		mockTG := harness.NewMockTelegramServer()
		defer mockTG.Close()

		targetID := "silenced-target"
		ruleID := "silenced-rule"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/maintenance",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
			cfg["alerting"].(map[string]any)["rules"] = []any{
				map[string]any{
					"name":     ruleID,
					"expr":     "probe_success == 0",
					"for":      "0s",
					"severity": "critical",
				},
			}
			cfg["alerting"].(map[string]any)["channels"] = []any{
				map[string]any{
					"name": "telegram",
					"type": "telegram",
					"config": map[string]any{
						"token":   "123:TOKEN",
						"chat_id": "999",
						"url":     mockTG.URL() + "/bot123:TOKEN/sendMessage",
					},
				},
			}
		}))

		env.StartServer(t)

		// Create an active silence covering this target
		silencePayload := map[string]any{
			"matchers":   fmt.Sprintf(`{"target":"%s"}`, targetID),
			"starts_at":  time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			"ends_at":    time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"comment":    "Silencing during planned maintenance",
			"created_by": "qa-team",
		}
		silResp := env.Client.PostJSON(t, "/api/v1/alerts/silence", silencePayload)
		silResp.AssertStatus(t, http.StatusCreated)

		// Wait for probe execution to complete
		pollTargetStatus(t, env.Client, targetID, 3*time.Second)

		// Telegram must have received 0 notification messages because of silence
		if mockTG.MessageCount() != 0 {
			t.Fatalf("expected 0 Telegram notifications due to active silence, got %d", mockTG.MessageCount())
		}
	})

	t.Run("TelegramMessageDispatch_Formatting", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()
		mockHTTP.SetDefaultResponse(http.StatusInternalServerError, "Outage")

		mockTG := harness.NewMockTelegramServer()
		defer mockTG.Close()

		targetID := "tg-notify-target"
		ruleID := "ServerOutageAlert"
		botToken := "998877:AAEEFFGG"
		chatID := "-1009876543210"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/down",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
			cfg["alerting"].(map[string]any)["rules"] = []any{
				map[string]any{
					"name":     ruleID,
					"expr":     "probe_success == 0",
					"for":      "0s",
					"severity": "critical",
				},
			}
			cfg["alerting"].(map[string]any)["channels"] = []any{
				map[string]any{
					"name": "telegram",
					"type": "telegram",
					"config": map[string]any{
						"token":   botToken,
						"chat_id": chatID,
						"url":     mockTG.URL() + fmt.Sprintf("/bot%s/sendMessage", botToken),
					},
				},
			}
		}))

		env.StartServer(t)

		// Wait for message on mock Telegram server
		msg, err := mockTG.WaitForMessage(3 * time.Second)
		if err != nil {
			t.Fatalf("failed to receive Telegram message: %v", err)
		}

		if msg.Token != botToken {
			t.Errorf("expected bot token %q, got %q", botToken, msg.Token)
		}
		if msg.ChatID != chatID {
			t.Errorf("expected chat ID %q, got %q", chatID, msg.ChatID)
		}
		if !strings.Contains(msg.Text, "CRITICAL") && !strings.Contains(msg.Text, ruleID) {
			t.Errorf("expected alert message to contain 'CRITICAL' and rule name, got:\n%s", msg.Text)
		}
	})

	t.Run("TelegramResolutionDispatch", func(t *testing.T) {
		mockHTTP := harness.NewMockHTTPServer()
		defer mockHTTP.Close()
		mockHTTP.SetDefaultResponse(http.StatusInternalServerError, "Outage")

		mockTG := harness.NewMockTelegramServer()
		defer mockTG.Close()

		targetID := "tg-resolution-target"
		ruleID := "ResolutionRule"
		botToken := "554433:XXYYZZ"
		chatID := "12345"

		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetID,
					"type":        "http",
					"target":      mockHTTP.URL() + "/status",
					"interval_ms": 100,
					"timeout_ms":  500,
				},
			}
			cfg["alerting"].(map[string]any)["rules"] = []any{
				map[string]any{
					"name":     ruleID,
					"expr":     "probe_success == 0",
					"for":      "0s",
					"severity": "warning",
				},
			}
			cfg["alerting"].(map[string]any)["channels"] = []any{
				map[string]any{
					"name": "telegram",
					"type": "telegram",
					"config": map[string]any{
						"token":   botToken,
						"chat_id": chatID,
						"url":     mockTG.URL() + fmt.Sprintf("/bot%s/sendMessage", botToken),
					},
				},
			}
		}))

		env.StartServer(t)

		// 1. Receive initial alert message
		_, err := mockTG.WaitForMessage(3 * time.Second)
		if err != nil {
			t.Fatalf("failed to receive initial alert: %v", err)
		}

		// 2. Service recovers
		mockHTTP.SetDefaultResponse(http.StatusOK, "Healthy")

		// 3. Wait for resolution message
		msgs, err := mockTG.WaitForMessages(2, 3*time.Second)
		if err != nil {
			t.Fatalf("failed to receive resolution message: %v", err)
		}

		resMsg := msgs[1]
		if !strings.Contains(strings.ToLower(resMsg.Text), "resolved") {
			t.Fatalf("expected second message to be a resolution notification, got:\n%s", resMsg.Text)
		}
	})
}
