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

// TelegramChannel sends alert notifications to the Telegram Bot API.
type TelegramChannel struct {
	name      string
	token     string
	chatID    string
	apiURL    string
	parseMode string
	client    *http.Client
	logger    *slog.Logger
}

// NewTelegramChannel constructs a Telegram notification channel from config.
func NewTelegramChannel(name string, cfg map[string]any, logger *slog.Logger) (*TelegramChannel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	token := getStringConfig(cfg, "token", "bot_token")
	chatID := getStringConfig(cfg, "chat_id")
	customURL := getStringConfig(cfg, "url", "api_url")
	parseMode := getStringConfig(cfg, "parse_mode")
	if parseMode == "" {
		parseMode = "HTML"
	}

	if token == "" && customURL == "" {
		return nil, errors.New("telegram channel requires token/bot_token or url")
	}
	if chatID == "" {
		return nil, errors.New("telegram channel requires chat_id")
	}

	endpoint := customURL
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	}

	return &TelegramChannel{
		name:      name,
		token:     token,
		chatID:    chatID,
		apiURL:    endpoint,
		parseMode: parseMode,
		client:    &http.Client{Timeout: 10 * time.Second},
		logger:    logger,
	}, nil
}

// Name returns the configured channel name.
func (t *TelegramChannel) Name() string { return t.name }

// Type returns "telegram".
func (t *TelegramChannel) Type() string { return "telegram" }

// Send formats and dispatches the AlertEvent to Telegram.
func (t *TelegramChannel) Send(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return errors.New("alert event cannot be nil")
	}

	text := formatTelegramMessage(event)
	payload := map[string]any{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": t.parseMode,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal telegram payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram send failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func formatTelegramMessage(event *models.AlertEvent) string {
	sev := strings.ToUpper(string(event.Severity))
	if event.Status == models.AlertStatusResolved {
		resolvedAt := time.Now().UTC()
		if event.ResolvedAt != nil {
			resolvedAt = event.ResolvedAt.UTC()
		}
		return fmt.Sprintf(
			"✅ <b>[RESOLVED] %s</b>\nSeverity: %s\nTarget: %s\nValue: %.4g\nResolved At: %s",
			event.RuleName,
			sev,
			event.TargetName,
			event.Value,
			resolvedAt.Format(time.RFC3339),
		)
	}

	icon := "⚠️"
	if event.Severity == models.SeverityCritical {
		icon = "🚨"
	} else if event.Severity == models.SeverityInfo {
		icon = "ℹ️"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>[%s] %s</b>\n", icon, sev, event.RuleName)
	fmt.Fprintf(&b, "Status: %s\n", strings.ToUpper(string(event.Status)))
	if event.TargetName != "" {
		fmt.Fprintf(&b, "Target: %s\n", event.TargetName)
	}
	fmt.Fprintf(&b, "Value: %.4g\n", event.Value)
	if event.Message != "" {
		fmt.Fprintf(&b, "Summary: %s\n", event.Message)
	}
	fmt.Fprintf(&b, "Fired At: %s", event.FiredAt.UTC().Format(time.RFC3339))
	return b.String()
}
