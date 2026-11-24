package prober

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Prober defines the uniform interface for executing a probe against a monitored target.
type Prober interface {
	Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error)
}

// Engine defines the prober orchestrator interface.
type Engine interface {
	Execute(ctx context.Context, target *models.Target) (*models.ProbeResult, error)
	RegisterProber(probeType models.TargetType, p Prober)
	GetProber(probeType models.TargetType) (Prober, bool)
	GetLastResult(targetID string) (*models.ProbeResult, bool)
	GetAllLastResults() map[string]*models.ProbeResult
}

// EngineOption configures a DefaultEngine instance.
type EngineOption func(*DefaultEngine)

// WithTSDB sets the TSDB storage instance where generated metric points are persisted.
func WithTSDB(tsdb storage.TSDB) EngineOption {
	return func(e *DefaultEngine) {
		e.tsdb = tsdb
	}
}

// WithMetrics sets the Prometheus metrics collector instance.
func WithMetrics(m *metrics.Metrics) EngineOption {
	return func(e *DefaultEngine) {
		e.metrics = m
	}
}

// DefaultEngine is the default thread-safe implementation of Engine.
type DefaultEngine struct {
	registry    Registry
	tsdb        storage.TSDB
	metrics     *metrics.Metrics
	mu          sync.RWMutex
	lastResults map[string]*models.ProbeResult
}

// NewEngine creates a new prober Engine with the given registry and options.
func NewEngine(reg Registry, opts ...EngineOption) *DefaultEngine {
	if reg == nil {
		reg = NewRegistry()
	}
	e := &DefaultEngine{
		registry:    reg,
		lastResults: make(map[string]*models.ProbeResult),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.metrics == nil {
		e.metrics = metrics.Default()
	}
	return e
}

// NewDefaultEngine creates a prober Engine pre-populated with default probers (HTTP, ICMP, TCP).
func NewDefaultEngine(opts ...EngineOption) *DefaultEngine {
	reg := NewRegistry()
	reg.Register(models.TargetTypeHTTP, NewHTTPProber())
	reg.Register(models.TargetTypeICMP, NewICMPProber())
	reg.Register(models.TargetTypeTCP, NewTCPProber())
	return NewEngine(reg, opts...)
}

// RegisterProber associates a Prober with a given target type.
func (e *DefaultEngine) RegisterProber(probeType models.TargetType, p Prober) {
	e.registry.Register(probeType, p)
}

// GetProber retrieves the registered Prober for a target type.
func (e *DefaultEngine) GetProber(probeType models.TargetType) (Prober, bool) {
	return e.registry.Get(probeType)
}

// GetLastResult returns the most recent probe execution result for a target ID.
func (e *DefaultEngine) GetLastResult(targetID string) (*models.ProbeResult, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res, ok := e.lastResults[targetID]
	return res, ok
}

// GetAllLastResults returns a snapshot of all latest probe execution results.
func (e *DefaultEngine) GetAllLastResults() map[string]*models.ProbeResult {
	e.mu.RLock()
	defer e.mu.RUnlock()
	results := make(map[string]*models.ProbeResult, len(e.lastResults))
	for k, v := range e.lastResults {
		results[k] = v
	}
	return results
}

// Execute validates the target, resolves the registered prober, executes the probe
// with target timeout enforcement, emits metrics, persists TSDB points, and caches result.
func (e *DefaultEngine) Execute(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	if target == nil {
		return nil, errors.New("target cannot be nil")
	}

	now := time.Now().UTC()

	// Validate target parameters
	if err := target.Validate(); err != nil {
		res := &models.ProbeResult{
			TargetID:   target.ID,
			TargetName: target.Name,
			TargetType: target.Type,
			Success:    false,
			Error:      fmt.Sprintf("target validation failed: %v", err),
			Timestamp:  now,
		}
		e.recordResult(res, string(target.Type))
		return res, err
	}

	// Resolve prober from registry
	p, ok := e.registry.Get(target.Type)
	if !ok {
		err := fmt.Errorf("unsupported probe type: %s", target.Type)
		res := &models.ProbeResult{
			TargetID:   target.ID,
			TargetName: target.Name,
			TargetType: target.Type,
			Success:    false,
			Error:      err.Error(),
			Timestamp:  now,
		}
		e.recordResult(res, string(target.Type))
		return res, err
	}

	// Apply target-configured timeout
	timeoutMs := target.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 10000
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	// Execute probe
	start := time.Now()
	res, probeErr := p.Probe(probeCtx, target)
	elapsed := time.Since(start)

	if res == nil {
		res = &models.ProbeResult{
			TargetID:     target.ID,
			TargetName:   target.Name,
			TargetType:   target.Type,
			Success:      false,
			ResponseTime: elapsed,
			Timestamp:    time.Now().UTC(),
		}
	}

	// Ensure essential fields are set
	if res.TargetID == "" {
		res.TargetID = target.ID
	}
	if res.TargetName == "" {
		res.TargetName = target.Name
	}
	if res.TargetType == "" {
		res.TargetType = target.Type
	}
	if res.Timestamp.IsZero() {
		res.Timestamp = time.Now().UTC()
	}
	if res.ResponseTime == 0 {
		res.ResponseTime = elapsed
	}

	// If prober returned an error or context timed out, record failure
	if probeErr != nil {
		res.Success = false
		if res.Error == "" {
			res.Error = probeErr.Error()
		}
	}
	if ctxErr := probeCtx.Err(); ctxErr != nil {
		res.Success = false
		if res.Error == "" {
			res.Error = ctxErr.Error()
		}
		if probeErr == nil {
			probeErr = ctxErr
		}
		updated := false
		for i := range res.Metrics {
			if res.Metrics[i].Metric == models.MetricProbeSuccess {
				res.Metrics[i].Value = 0.0
				updated = true
			}
		}
		if !updated {
			res.Metrics = append(res.Metrics, models.MetricPoint{
				TargetID:  res.TargetID,
				Metric:    models.MetricProbeSuccess,
				Value:     0.0,
				Timestamp: res.Timestamp,
			})
		}
	}

	e.recordResult(res, string(target.Type))
	return res, probeErr
}

// recordResult updates internal Prometheus metrics, persists metric points to TSDB,
// and saves the latest result to cache.
func (e *DefaultEngine) recordResult(res *models.ProbeResult, targetType string) {
	if targetType == "" {
		targetType = string(res.TargetType)
	}

	// 1. Prometheus metrics
	if e.metrics != nil {
		e.metrics.RecordProbe(targetType, res.Success, res.ResponseTime)
		if !res.Success {
			reason := "probe_failure"
			if res.Error != "" {
				reason = "error"
			}
			e.metrics.RecordProbeError(targetType, reason)
		}
	}

	// 2. Persist points to TSDB
	if e.tsdb != nil {
		points := res.ToMetricPoints()
		_ = e.tsdb.WritePoints(points)
	}

	// 3. Cache latest result
	if res.TargetID != "" {
		e.mu.Lock()
		e.lastResults[res.TargetID] = res
		e.mu.Unlock()
	}
}
