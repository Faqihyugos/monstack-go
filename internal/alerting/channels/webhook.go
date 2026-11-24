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

// WebhookChannel dispatches alert events as JSON HTTP POST requests.
type WebhookChannel struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client
	logger  *slog.Logger
}

// NewWebhookChannel constructs a Webhook notification channel from config.
func NewWebhookChannel(name string, cfg map[string]any, logger *slog.Logger) (*WebhookChannel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	u := getStringConfig(cfg, "url", "webhook_url")
	if u == "" {
		return nil, errors.New("webhook channel requires url")
	}

	headers := make(map[string]string)
	if rawHeaders, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range rawHeaders {
			headers[k] = fmt.Sprintf("%v", v)
		}
	}

	return &WebhookChannel{
		name:    name,
		url:     u,
		headers: headers,
		client:  &http.Client{Timeout: 10 * time.Second},
		logger:  logger,
	}, nil
}

// Name returns the configured channel name.
func (w *WebhookChannel) Name() string { return w.name }

// Type returns "webhook".
func (w *WebhookChannel) Type() string { return "webhook" }

// Send posts the AlertEvent as JSON to the configured webhook URL.
func (w *WebhookChannel) Send(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return errors.New("alert event cannot be nil")
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook send failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}
