package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Faqihyugos/monstack-go/internal/alerting/channels"
	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Router dispatches AlertEvents to matched notification channels.
type Router struct {
	mu       sync.RWMutex
	channels map[string]channels.Channel
	routing  config.AlertRoutingConfig
	metrics  *metrics.Metrics
	logger   *slog.Logger
}

// NewRouter creates a Router initialized from alerting configuration.
func NewRouter(cfg config.AlertingConfig, m *metrics.Metrics, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default()
	}
	r := &Router{
		channels: make(map[string]channels.Channel),
		metrics:  m,
		logger:   logger,
	}
	r.UpdateConfig(cfg)
	return r
}

// RegisterChannel registers or replaces a notification channel by name.
func (r *Router) RegisterChannel(ch channels.Channel) {
	if ch == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[ch.Name()] = ch
}

// UpdateConfig rebuilds channels and routing rules from configuration.
func (r *Router) UpdateConfig(cfg config.AlertingConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	newChannels := make(map[string]channels.Channel, len(cfg.Channels))
	for _, chCfg := range cfg.Channels {
		ch, err := channels.NewChannel(chCfg, r.logger)
		if err != nil {
			r.logger.Warn("skipping invalid notification channel", "name", chCfg.Name, "type", chCfg.Type, "err", err)
			continue
		}
		newChannels[ch.Name()] = ch
	}
	r.channels = newChannels
	r.routing = cfg.Routing
}

// ResolveChannels determines which channels should receive an alert for the given rule and labels.
func (r *Router) ResolveChannels(rule *models.AlertRule, labels map[string]string) []channels.Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.channels) == 0 {
		return nil
	}

	selectedNames := make([]string, 0)
	seen := make(map[string]struct{})
	addName := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; !ok {
			if _, exists := r.channels[name]; exists {
				seen[name] = struct{}{}
				selectedNames = append(selectedNames, name)
			}
		}
	}

	// 1. Explicit channels on rule take precedence
	if rule != nil && len(rule.Channels) > 0 {
		for _, name := range rule.Channels {
			addName(name)
		}
	}

	// 2. Match routing rules
	if len(selectedNames) == 0 {
		for _, route := range r.routing.Routes {
			if matchesLabels(route.Match, labels) {
				for _, name := range route.Channels {
					addName(name)
				}
			}
		}
	}

	// 3. Default channel
	if len(selectedNames) == 0 && r.routing.DefaultChannel != "" {
		addName(r.routing.DefaultChannel)
	}

	// 4. Fallback: if no routing/default specified, deliver to all configured channels
	if len(selectedNames) == 0 {
		for name := range r.channels {
			addName(name)
		}
	}

	res := make([]channels.Channel, 0, len(selectedNames))
	for _, name := range selectedNames {
		res = append(res, r.channels[name])
	}
	return res
}

// Dispatch sends an alert event to all resolved notification channels.
// Returns the comma-separated list of channel names attempted and any combined error.
func (r *Router) Dispatch(ctx context.Context, rule *models.AlertRule, event *models.AlertEvent, labels map[string]string) (string, error) {
	chs := r.ResolveChannels(rule, labels)
	if len(chs) == 0 {
		return "", nil
	}

	var sentNames []string
	var errs []string
	for _, ch := range chs {
		sentNames = append(sentNames, ch.Name())
		if err := ch.Send(ctx, event); err != nil {
			r.logger.Error("alert notification failed", "channel", ch.Name(), "rule", event.RuleName, "target", event.TargetName, "err", err)
			if r.metrics != nil {
				r.metrics.RecordAlertSent(ch.Name(), "failure")
			}
			errs = append(errs, fmt.Sprintf("%s: %v", ch.Name(), err))
		} else {
			r.logger.Info("alert notification sent", "channel", ch.Name(), "rule", event.RuleName, "target", event.TargetName, "status", event.Status)
			if r.metrics != nil {
				r.metrics.RecordAlertSent(ch.Name(), "success")
			}
		}
	}

	names := strings.Join(sentNames, ",")
	if len(errs) > 0 {
		return names, fmt.Errorf("notification errors: %s", strings.Join(errs, "; "))
	}
	return names, nil
}

func matchesLabels(match map[string]string, labels map[string]string) bool {
	if len(match) == 0 {
		return false
	}
	for k, v := range match {
		if labels[k] != v {
			return false
		}
	}
	return true
}
