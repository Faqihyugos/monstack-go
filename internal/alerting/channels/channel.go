package channels

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Channel defines the interface for delivering alert notifications.
type Channel interface {
	Name() string
	Type() string
	Send(ctx context.Context, event *models.AlertEvent) error
}

// NewChannel instantiates a notification channel from configuration.
func NewChannel(cfg config.AlertChannelConfig, logger *slog.Logger) (Channel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	chType := strings.ToLower(strings.TrimSpace(cfg.Type))
	switch chType {
	case "telegram":
		return NewTelegramChannel(cfg.Name, cfg.Config, logger)
	case "webhook":
		return NewWebhookChannel(cfg.Name, cfg.Config, logger)
	case "slack":
		return NewSlackChannel(cfg.Name, cfg.Config, logger)
	case "discord":
		return NewDiscordChannel(cfg.Name, cfg.Config, logger)
	case "email":
		return NewEmailChannel(cfg.Name, cfg.Config, logger)
	case "pagerduty":
		return NewPagerDutyChannel(cfg.Name, cfg.Config, logger)
	default:
		return nil, fmt.Errorf("unsupported notification channel type: %q", cfg.Type)
	}
}

func getStringConfig(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s != "" {
				return s
			}
		}
	}
	return ""
}
