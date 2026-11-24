package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

const defaultPagerDutyURL = "https://events.pagerduty.com/v2/enqueue"

// PagerDutyChannel dispatches alerts via PagerDuty Events API v2.
type PagerDutyChannel struct {
	name       string
	routingKey string
	apiURL     string
	client     *http.Client
	logger     *slog.Logger
}

// NewPagerDutyChannel creates a PagerDuty Events API v2 channel from config.
func NewPagerDutyChannel(name string, cfg map[string]any, logger *slog.Logger) (*PagerDutyChannel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	key := getStringConfig(cfg, "routing_key", "service_key")
	if key == "" {
		return nil, errors.New("pagerduty channel requires routing_key or service_key")
	}
	apiURL := getStringConfig(cfg, "url", "api_url")
	if apiURL == "" {
		apiURL = defaultPagerDutyURL
	}
	return &PagerDutyChannel{
		name:       name,
		routingKey: key,
		apiURL:     apiURL,
		client:     &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}, nil
}

func (p *PagerDutyChannel) Name() string { return p.name }
func (p *PagerDutyChannel) Type() string { return "pagerduty" }

// Send triggers or resolves a PagerDuty incident using dedup_key = ruleID:targetName.
func (p *PagerDutyChannel) Send(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return errors.New("alert event cannot be nil")
	}

	action := "trigger"
	if event.Status == models.AlertStatusResolved {
		action = "resolve"
	}

	pdSev := "warning"
	switch event.Severity {
	case models.SeverityCritical:
		pdSev = "critical"
	case models.SeverityInfo:
		pdSev = "info"
	}

	summary := event.Message
	if summary == "" {
		summary = fmt.Sprintf("[%s] %s on %s", strings.ToUpper(string(event.Severity)), event.RuleName, event.TargetName)
	}

	payload := map[string]any{
		"routing_key":  p.routingKey,
		"event_action": action,
		"dedup_key":    fmt.Sprintf("monstack:%s:%s", event.RuleID, event.TargetName),
		"payload": map[string]any{
			"summary":   summary,
			"source":    event.TargetName,
			"severity":  pdSev,
			"timestamp": event.FiredAt.UTC().Format(time.RFC3339),
			"custom_details": map[string]any{
				"rule_name": event.RuleName,
				"value":     event.Value,
				"status":    string(event.Status),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal pagerduty payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create pagerduty request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("pagerduty send failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pagerduty returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
