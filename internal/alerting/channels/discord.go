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

// DiscordChannel dispatches rich embed alerts to a Discord Webhook.
type DiscordChannel struct {
	name       string
	webhookURL string
	username   string
	client     *http.Client
	logger     *slog.Logger
}

// NewDiscordChannel creates a Discord notification channel from config.
func NewDiscordChannel(name string, cfg map[string]any, logger *slog.Logger) (*DiscordChannel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	u := getStringConfig(cfg, "webhook_url", "url")
	if u == "" {
		return nil, errors.New("discord channel requires webhook_url")
	}
	username := getStringConfig(cfg, "username")
	if username == "" {
		username = "MonStack-Go"
	}
	return &DiscordChannel{
		name:       name,
		webhookURL: u,
		username:   username,
		client:     &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}, nil
}

func (d *DiscordChannel) Name() string { return d.name }
func (d *DiscordChannel) Type() string { return "discord" }

// Send posts a Discord webhook embed message.
func (d *DiscordChannel) Send(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return errors.New("alert event cannot be nil")
	}

	color := 0xf59e0b // warning
	title := fmt.Sprintf("[%s] %s", strings.ToUpper(string(event.Severity)), event.RuleName)
	if event.Status == models.AlertStatusResolved {
		color = 0x10b981 // resolved green
		title = fmt.Sprintf("[RESOLVED] %s", event.RuleName)
	} else if event.Severity == models.SeverityCritical {
		color = 0xef4444 // critical red
	} else if event.Severity == models.SeverityInfo {
		color = 0x3b82f6 // info blue
	}

	payload := map[string]any{
		"username": d.username,
		"embeds": []map[string]any{
			{
				"title":       title,
				"description": event.Message,
				"color":       color,
				"fields": []map[string]any{
					{"name": "Target", "value": event.TargetName, "inline": true},
					{"name": "Status", "value": strings.ToUpper(string(event.Status)), "inline": true},
					{"name": "Value", "value": fmt.Sprintf("%.4g", event.Value), "inline": true},
				},
				"timestamp": event.FiredAt.UTC().Format(time.RFC3339),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("discord send failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
