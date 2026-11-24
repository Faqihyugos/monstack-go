package channels_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/alerting/channels"
	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

func sampleEvent(status models.AlertStatus) *models.AlertEvent {
	now := time.Now().UTC()
	ev := &models.AlertEvent{
		RuleID:     "rule-1",
		RuleName:   "API Down",
		TargetName: "prod-api",
		Status:     status,
		Severity:   models.SeverityCritical,
		Value:      0,
		Message:    "prod-api is unreachable",
		FiredAt:    now,
	}
	if status == models.AlertStatusResolved {
		ev.ResolvedAt = &now
	}
	return ev
}

func TestSlackChannel(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := channels.NewChannel(config.AlertChannelConfig{
		Name: "ops-slack",
		Type: "slack",
		Config: map[string]any{
			"webhook_url": srv.URL,
			"channel":     "#alerts",
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewChannel(slack): %v", err)
	}

	if err := ch.Send(context.Background(), sampleEvent(models.AlertStatusFiring)); err != nil {
		t.Fatalf("Slack Send: %v", err)
	}
	if received["channel"] != "#alerts" {
		t.Errorf("expected channel #alerts, got %v", received["channel"])
	}
}

func TestDiscordChannel(t *testing.T) {
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ch, err := channels.NewChannel(config.AlertChannelConfig{
		Name: "ops-discord",
		Type: "discord",
		Config: map[string]any{
			"webhook_url": srv.URL,
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewChannel(discord): %v", err)
	}

	if err := ch.Send(context.Background(), sampleEvent(models.AlertStatusFiring)); err != nil {
		t.Fatalf("Discord Send: %v", err)
	}
	if !strings.Contains(receivedBody, "API Down") || !strings.Contains(receivedBody, "prod-api") {
		t.Errorf("unexpected discord payload: %s", receivedBody)
	}
}

func TestPagerDutyChannel(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if a, ok := payload["event_action"].(string); ok {
			actions = append(actions, a)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	ch, err := channels.NewChannel(config.AlertChannelConfig{
		Name: "pd-crit",
		Type: "pagerduty",
		Config: map[string]any{
			"routing_key": "test-routing-key",
			"url":         srv.URL,
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewChannel(pagerduty): %v", err)
	}

	if err := ch.Send(context.Background(), sampleEvent(models.AlertStatusFiring)); err != nil {
		t.Fatalf("PagerDuty trigger: %v", err)
	}
	if err := ch.Send(context.Background(), sampleEvent(models.AlertStatusResolved)); err != nil {
		t.Fatalf("PagerDuty resolve: %v", err)
	}
	if len(actions) != 2 || actions[0] != "trigger" || actions[1] != "resolve" {
		t.Fatalf("expected [trigger resolve], got %v", actions)
	}
}

func TestEmailChannelValidation(t *testing.T) {
	_, err := channels.NewChannel(config.AlertChannelConfig{
		Name: "ops-email",
		Type: "email",
		Config: map[string]any{
			"smtp_host": "smtp.example.com",
			"smtp_port": "587",
			"from":      "alerts@example.com",
			"to":        []string{"ops@example.com"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("expected valid email channel config, got: %v", err)
	}
}
