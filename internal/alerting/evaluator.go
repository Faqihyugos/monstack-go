package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// ParsedExpr represents a simple threshold comparison: <metric> <op> <threshold>.
type ParsedExpr struct {
	Metric    string
	Operator  string
	Threshold float64
}

// ParseExpression parses expressions such as:
// "probe_success == 0", "probe_response_time > 2s", "probe_ssl_days_remaining < 14".
func ParseExpression(expr string) (*ParsedExpr, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty alert expression")
	}

	// Order matters: check two-char operators before single-char operators.
	operators := []string{"==", "!=", ">=", "<=", ">", "<"}
	for _, op := range operators {
		if idx := strings.Index(expr, op); idx != -1 {
			metric := strings.TrimSpace(expr[:idx])
			valStr := strings.TrimSpace(expr[idx+len(op):])
			if metric == "" || valStr == "" {
				return nil, fmt.Errorf("invalid alert expression: %q", expr)
			}
			threshold, err := parseThresholdValue(valStr)
			if err != nil {
				return nil, fmt.Errorf("invalid threshold in expression %q: %w", expr, err)
			}
			return &ParsedExpr{
				Metric:    metric,
				Operator:  op,
				Threshold: threshold,
			}, nil
		}
	}

	return nil, fmt.Errorf("unsupported operator in alert expression: %q", expr)
}

func parseThresholdValue(s string) (float64, error) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d.Seconds(), nil
	}
	return 0, fmt.Errorf("cannot parse %q as number or duration", s)
}

// Evaluate compares value against threshold using the parsed operator.
func (p *ParsedExpr) Evaluate(val float64) bool {
	switch p.Operator {
	case "==":
		return val == p.Threshold
	case "!=":
		return val != p.Threshold
	case ">":
		return val > p.Threshold
	case ">=":
		return val >= p.Threshold
	case "<":
		return val < p.Threshold
	case "<=":
		return val <= p.Threshold
	default:
		return false
	}
}

type alertState struct {
	status       models.AlertStatus
	pendingSince time.Time
	event        *models.AlertEvent
}

// Evaluator periodically evaluates alert rules against latest probe metrics and manages alert lifecycle.
type Evaluator struct {
	meta     storage.MetadataStore
	tsdb     storage.TSDB
	prober   prober.Engine
	router   *Router
	silencer *Silencer
	metrics  *metrics.Metrics
	logger   *slog.Logger

	mu       sync.Mutex
	interval time.Duration
	states   map[string]*alertState // key: ruleID + ":" + targetID

	triggerCh chan struct{}
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewEvaluator creates a new alert rule evaluator.
func NewEvaluator(
	meta storage.MetadataStore,
	tsdb storage.TSDB,
	engine prober.Engine,
	router *Router,
	silencer *Silencer,
	m *metrics.Metrics,
	interval time.Duration,
	logger *slog.Logger,
) *Evaluator {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default()
	}
	return &Evaluator{
		meta:     meta,
		tsdb:     tsdb,
		prober:   engine,
		router:   router,
		silencer: silencer,
		metrics:  m,
		logger:   logger,
		interval:  interval,
		states:    make(map[string]*alertState),
		triggerCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Trigger requests an immediate evaluation cycle (non-blocking).
func (e *Evaluator) Trigger() {
	if e == nil || e.triggerCh == nil {
		return
	}
	select {
	case e.triggerCh <- struct{}{}:
	default:
	}
}

// SetInterval updates the evaluation tick interval (used on hot-reload).
func (e *Evaluator) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	e.mu.Lock()
	e.interval = d
	e.mu.Unlock()
}

// Start runs the evaluation loop until ctx is cancelled or Stop is called.
func (e *Evaluator) Start(ctx context.Context) {
	go func() {
		defer close(e.doneCh)

		// Brief startup grace period so initial probe results and silences settle
		startupTimer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			startupTimer.Stop()
			return
		case <-e.stopCh:
			startupTimer.Stop()
			return
		case <-startupTimer.C:
		}

		e.EvaluateOnce(ctx)

		for {
			e.mu.Lock()
			interval := e.interval
			e.mu.Unlock()

			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-e.stopCh:
				timer.Stop()
				return
			case <-e.triggerCh:
				timer.Stop()
				e.EvaluateOnce(ctx)
			case <-timer.C:
				e.EvaluateOnce(ctx)
			}
		}
	}()
}

// Stop halts the evaluation loop and waits for it to exit.
func (e *Evaluator) Stop() {
	e.stopOnce.Do(func() {
		close(e.stopCh)
	})
	<-e.doneCh
}

// EvaluateOnce performs a single evaluation pass across all enabled alert rules and targets.
func (e *Evaluator) EvaluateOnce(ctx context.Context) {
	if e.meta == nil {
		return
	}

	enabled := true
	rules, err := e.meta.ListAlertRules(ctx, &enabled)
	if err != nil {
		e.logger.Error("failed to list alert rules", "err", err)
		return
	}
	if len(rules) == 0 {
		return
	}

	targets, err := e.meta.ListTargets(ctx, "", &enabled)
	if err != nil {
		e.logger.Error("failed to list targets for alerting", "err", err)
		return
	}

	now := time.Now().UTC()
	activeBySeverity := map[string]float64{
		string(models.SeverityInfo):     0,
		string(models.SeverityWarning):  0,
		string(models.SeverityCritical): 0,
	}

	for _, rule := range rules {
		parsed, err := ParseExpression(rule.Expression)
		if err != nil {
			e.logger.Warn("skipping rule with invalid expression", "rule", rule.Name, "expr", rule.Expression, "err", err)
			continue
		}

		for _, target := range targets {
			if rule.TargetMatch != "" && !strings.Contains(target.Name, rule.TargetMatch) && target.ID != rule.TargetMatch {
				continue
			}

			val, ok := e.lookupMetricValue(target.ID, parsed.Metric)
			if !ok {
				continue
			}

			triggered := parsed.Evaluate(val)
			e.processTransition(ctx, rule, target, val, triggered, now)
		}
	}

	// Update active alert gauges
	e.mu.Lock()
	for _, st := range e.states {
		if st.status == models.AlertStatusFiring && st.event != nil {
			activeBySeverity[string(st.event.Severity)]++
		}
	}
	e.mu.Unlock()

	if e.metrics != nil {
		for sev, count := range activeBySeverity {
			e.metrics.SetActiveAlerts(sev, count)
		}
	}
}

func (e *Evaluator) lookupMetricValue(targetID, metricName string) (float64, bool) {
	// Normalize common metric aliases
	candidates := []string{metricName}
	switch metricName {
	case "probe_http_response_time", "probe_response_time":
		candidates = []string{metricName, models.MetricProbeDurationSeconds, models.MetricProbeResponseTime}
	case "probe_ssl_days_remaining":
		candidates = []string{metricName, models.MetricHTTPSSLExpiryDays}
	case "probe_status_code", "status_code":
		candidates = []string{metricName, models.MetricHTTPStatusCode}
	}

	// 1. Check prober latest cached result first (most immediate)
	if e.prober != nil {
		if res, ok := e.prober.GetLastResult(targetID); ok && res != nil {
			for _, cand := range candidates {
				if cand == models.MetricProbeSuccess {
					if res.Success {
						return 1.0, true
					}
					return 0.0, true
				}
				if cand == models.MetricProbeDurationSeconds || cand == "probe_response_time" || cand == "probe_http_response_time" {
					return res.ResponseTime.Seconds(), true
				}
				for _, pt := range res.Metrics {
					if pt.Metric == cand {
						return pt.Value, true
					}
				}
			}
		}
	}

	// 2. Fallback to TSDB latest point
	if e.tsdb != nil {
		for _, cand := range candidates {
			if pt, err := e.tsdb.GetLatestPoint(targetID, cand); err == nil && pt != nil {
				return pt.Value, true
			}
		}
	}

	return 0, false
}

func (e *Evaluator) processTransition(
	ctx context.Context,
	rule *models.AlertRule,
	target *models.Target,
	val float64,
	triggered bool,
	now time.Time,
) {
	key := rule.ID + ":" + target.ID
	forDur := rule.ForDurationParsed()

	e.mu.Lock()
	st, exists := e.states[key]
	if !triggered {
		// Condition cleared
		if exists && st.status == models.AlertStatusFiring && st.event != nil {
			ev := st.event
			delete(e.states, key)
			e.mu.Unlock()
			e.resolveAlert(ctx, rule, target, ev, val, now)
			return
		}
		if exists {
			delete(e.states, key)
		}
		e.mu.Unlock()
		return
	}

	// Condition is triggered
	if !exists {
		if forDur <= 0 {
			ev := e.buildAlertEvent(rule, target, val, models.AlertStatusFiring, now)
			e.states[key] = &alertState{
				status:       models.AlertStatusFiring,
				pendingSince: now,
				event:        ev,
			}
			e.mu.Unlock()
			e.fireAlert(ctx, rule, target, ev, now)
			return
		}
		e.states[key] = &alertState{
			status:       models.AlertStatusPending,
			pendingSince: now,
		}
		e.mu.Unlock()
		return
	}

	// Already exists and still triggered
	if st.status == models.AlertStatusPending {
		if now.Sub(st.pendingSince) >= forDur {
			ev := e.buildAlertEvent(rule, target, val, models.AlertStatusFiring, now)
			st.status = models.AlertStatusFiring
			st.event = ev
			e.mu.Unlock()
			e.fireAlert(ctx, rule, target, ev, now)
			return
		}
	}
	e.mu.Unlock()
}

func (e *Evaluator) buildAlertEvent(
	rule *models.AlertRule,
	target *models.Target,
	val float64,
	status models.AlertStatus,
	now time.Time,
) *models.AlertEvent {
	msg := ""
	if rule.Annotations != nil {
		if summary, ok := rule.Annotations["summary"]; ok {
			msg = summary
		}
	}
	if msg == "" {
		msg = fmt.Sprintf("Rule %q triggered for target %q (value=%.4g)", rule.Name, target.Name, val)
	} else {
		msg = strings.ReplaceAll(msg, "{{ $target }}", target.Name)
		msg = strings.ReplaceAll(msg, "{{ $value }}", fmt.Sprintf("%.4g", val))
	}

	return &models.AlertEvent{
		RuleID:             rule.ID,
		RuleName:           rule.Name,
		TargetName:         target.Name,
		TargetAddress:      target.Target,
		Status:             status,
		Severity:           rule.Severity,
		Value:              val,
		Message:            msg,
		FiredAt:            now,
		NotificationStatus: models.NotificationStatusPending,
	}
}

func (e *Evaluator) buildLabels(rule *models.AlertRule, target *models.Target) map[string]string {
	labels := make(map[string]string)
	for k, v := range target.Labels {
		labels[k] = v
	}
	for k, v := range rule.Labels {
		labels[k] = v
	}
	labels["severity"] = string(rule.Severity)
	labels["target"] = target.Name
	labels["target_name"] = target.Name
	labels["rule"] = rule.Name
	return labels
}

func (e *Evaluator) fireAlert(ctx context.Context, rule *models.AlertRule, target *models.Target, ev *models.AlertEvent, now time.Time) {
	labels := e.buildLabels(rule, target)

	if e.silencer != nil && e.silencer.IsSilenced(ctx, target.Name, labels, now) {
		ev.NotificationStatus = models.NotificationStatusSkipped
		e.logger.Info("alert notification silenced", "rule", rule.Name, "target", target.Name)
	} else if e.router != nil {
		chNames, err := e.router.Dispatch(ctx, rule, ev, labels)
		ev.NotificationChannel = chNames
		if chNames == "" {
			ev.NotificationStatus = models.NotificationStatusSkipped
		} else if err != nil {
			ev.NotificationStatus = models.NotificationStatusFailed
		} else {
			ev.NotificationStatus = models.NotificationStatusSent
			notifiedAt := time.Now().UTC()
			ev.NotifiedAt = &notifiedAt
		}
	}

	if err := e.meta.RecordAlertEvent(ctx, ev); err != nil {
		e.logger.Error("failed to persist firing alert event", "rule", rule.Name, "target", target.Name, "err", err)
	}
}

func (e *Evaluator) resolveAlert(ctx context.Context, rule *models.AlertRule, target *models.Target, ev *models.AlertEvent, val float64, now time.Time) {
	ev.Status = models.AlertStatusResolved
	ev.Value = val
	resolvedAt := now
	ev.ResolvedAt = &resolvedAt

	if err := e.meta.UpdateAlertEvent(ctx, ev); err != nil {
		e.logger.Error("failed to update resolved alert event", "rule", rule.Name, "target", target.Name, "err", err)
	}

	labels := e.buildLabels(rule, target)
	if e.silencer != nil && e.silencer.IsSilenced(ctx, target.Name, labels, now) {
		return
	}
	if e.router != nil {
		_, _ = e.router.Dispatch(ctx, rule, ev, labels)
	}
}
