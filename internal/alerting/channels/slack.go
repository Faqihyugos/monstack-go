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

// SlackChannel dispatches alert notifications to a Slack Incoming Webhook.
type SlackChannel struct {
	name       string
	webhookURL string
	channel    string
	username   string
	client     *http.Client
	logger     *slog.Logger
}

// NewSlackChannel creates a Slack notification channel from config.
func NewSlackChannel(name string, cfg map[string]any, logger *slog.Logger) (*SlackChannel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	u := getStringConfig(cfg, "webhook_url", "url")
	if u == "" {
		return nil, errors.New("slack channel requires webhook_url")
	}
	username := getStringConfig(cfg, "username")
	if username == "" {
		username = "MonStack-Go"
	}
	return &SlackChannel{
		name:       name,
		webhookURL: u,
		channel:    getStringConfig(cfg, "channel"),
		username:   username,
		client:     &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}, nil
}

func (s *SlackChannel) Name() string { return s.name }
func (s *SlackChannel) Type() string { return "slack" }

// Send posts a color-coded Slack attachment payload.
func (s *SlackChannel) Send(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return errors.New("alert event cannot be nil")
	}

	color := "#f59e0b" // warning amber
	title := fmt.Sprintf("[%s] %s", strings.ToUpper(string(event.Severity)), event.RuleName)
	if event.Status == models.AlertStatusResolved {
		color = "#10b981" // resolved green
		title = fmt.Sprintf("[RESOLVED] %s", event.RuleName)
	} else if event.Severity == models.SeverityCritical {
		color = "#ef4444" // critical red
	} else if event.Severity == models.SeverityInfo {
		color = "#3b82f6" // info blue
	}

	payload := map[string]any{
		"username": s.username,
		"attachments": []map[string]any{
			{
				"color": color,
				"title": title,
				"text":  event.Message,
				"fields": []map[string]any{
					{"title": "Target", "value": event.TargetName, "short": true},
					{"title": "Status", "value": strings.ToUpper(string(event.Status)), "short": true},
					{"title": "Value", "value": fmt.Sprintf("%.4g", event.Value), "short": true},
					{"title": "Severity", "value": strings.ToUpper(string(event.Severity)), "short": true},
				},
				"ts": event.FiredAt.Unix(),
			},
		},
	}
	if s.channel != "" {
		payload["channel"] = s.channel
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack send failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
