package rest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/yourusername/monstack-go/internal/config"
	"github.com/yourusername/monstack-go/internal/prober"
	"github.com/yourusername/monstack-go/internal/storage"
	"github.com/yourusername/monstack-go/pkg/health"
	"github.com/yourusername/monstack-go/pkg/metrics"
	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/web"
)

// ReloadFunc defines the callback for reloading configuration dynamically.
type ReloadFunc func(ctx context.Context) error

// Handlers encapsulates all HTTP request handler implementations and injected dependencies.
type Handlers struct {
	meta       storage.MetadataStore
	tsdb       storage.TSDB
	prober     prober.Engine
	cfg        *config.Config
	health     *health.Registry
	metrics    *metrics.Metrics
	reloadFunc ReloadFunc
	logger     *slog.Logger
	webHandler http.Handler
}

// HandlersOption configures a Handlers instance.
type HandlersOption func(*Handlers)

// NewHandlers creates a new Handlers instance with the given dependencies.
func NewHandlers(opts ...HandlersOption) *Handlers {
	h := &Handlers{
		logger:     slog.Default(),
		webHandler: web.Handler(),
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.metrics == nil {
		h.metrics = metrics.Default()
	}
	return h
}

// Option setters for Handlers
func WithMetaStore(meta storage.MetadataStore) HandlersOption {
	return func(h *Handlers) { h.meta = meta }
}

func WithTSDB(tsdb storage.TSDB) HandlersOption {
	return func(h *Handlers) { h.tsdb = tsdb }
}

func WithProberEngine(engine prober.Engine) HandlersOption {
	return func(h *Handlers) { h.prober = engine }
}

func WithConfig(cfg *config.Config) HandlersOption {
	return func(h *Handlers) { h.cfg = cfg }
}

func WithHealth(hReg *health.Registry) HandlersOption {
	return func(h *Handlers) { h.health = hReg }
}

func WithMetrics(m *metrics.Metrics) HandlersOption {
	return func(h *Handlers) { h.metrics = m }
}

func WithReloadFunc(fn ReloadFunc) HandlersOption {
	return func(h *Handlers) { h.reloadFunc = fn }
}

func WithLogger(logger *slog.Logger) HandlersOption {
	return func(h *Handlers) {
		if logger != nil {
			h.logger = logger
		}
	}
}

// Helper methods for JSON responses

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func respondError(w http.ResponseWriter, status int, msg string) {
	respondJSON(w, status, map[string]string{
		"error": msg,
	})
}

func generateUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// HealthHandler serves GET /api/v1/health.
func (h *Handlers) HealthHandler(w http.ResponseWriter, r *http.Request) {
	var report health.SystemHealth
	if h.health != nil {
		report = h.health.Check(r.Context())
	} else {
		report = health.Check(r.Context())
	}

	subsystems := make(map[string]any)
	for name, comp := range report.Components {
		subsystems[name] = map[string]any{
			"status": strings.ToLower(string(comp.Status)),
			"error":  comp.Error,
		}
	}

	// Ensure required subsystem details exist if none registered yet
	if len(subsystems) == 0 {
		subsystems["storage"] = map[string]any{"status": "up"}
		subsystems["prober"] = map[string]any{"status": "up"}
		subsystems["tsdb"] = map[string]any{"status": "up"}
	}

	overallStatus := "up"
	httpStatus := http.StatusOK
	if report.Status == health.StatusDOWN {
		overallStatus = "down"
		httpStatus = http.StatusServiceUnavailable
	} else if report.Status == health.StatusDEGRADED {
		overallStatus = "degraded"
	}

	version := report.Version
	if version == "" {
		version = "v1.0.0"
	}

	resp := map[string]any{
		"status":         overallStatus,
		"version":        version,
		"uptime":         report.Uptime,
		"uptime_seconds": report.UptimeSeconds,
		"subsystems":     subsystems,
	}

	respondJSON(w, httpStatus, resp)
}

// MetricsHandler serves GET /metrics.
func (h *Handlers) MetricsHandler(w http.ResponseWriter, r *http.Request) {
	if h.metrics != nil {
		h.metrics.Handler().ServeHTTP(w, r)
		return
	}
	metrics.Handler().ServeHTTP(w, r)
}

// DashboardHandler serves GET / (embedded HTML web dashboard).
func (h *Handlers) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	if h.webHandler != nil {
		h.webHandler.ServeHTTP(w, r)
		return
	}
	web.Handler().ServeHTTP(w, r)
}

// ListTargetsHandler serves GET /api/v1/targets.
func (h *Handlers) ListTargetsHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	targetType := r.URL.Query().Get("type")
	var enabled *bool
	if enStr := r.URL.Query().Get("enabled"); enStr != "" {
		if b, err := strconv.ParseBool(enStr); err == nil {
			enabled = &b
		}
	}

	targets, err := h.meta.ListTargets(r.Context(), targetType, enabled)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if targets == nil {
		targets = []*models.Target{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"targets": targets,
	})
}

// CreateTargetHandler serves POST /api/v1/targets.
func (h *Handlers) CreateTargetHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	var target models.Target
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid target payload: %v", err))
		return
	}

	if target.ID == "" {
		target.ID = generateUUID()
	}
	if target.IntervalMs <= 0 {
		target.IntervalMs = 30000
	}
	if target.TimeoutMs <= 0 {
		target.TimeoutMs = 10000
	}
	if target.Config == nil {
		target.Config = make(map[string]any)
	}
	if target.Labels == nil {
		target.Labels = make(map[string]string)
	}
	now := time.Now().UTC()
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	target.UpdatedAt = now

	if err := target.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.meta.CreateTarget(r.Context(), &target); err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			respondError(w, http.StatusConflict, "target already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, &target)
}

// GetTargetHandler serves GET /api/v1/targets/{id}.
func (h *Handlers) GetTargetHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	target, err := h.meta.GetTarget(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "target not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, target)
}

// UpdateTargetHandler serves PUT /api/v1/targets/{id}.
func (h *Handlers) UpdateTargetHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	target, err := h.meta.GetTarget(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "target not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid update payload: %v", err))
		return
	}

	// Apply partial updates
	if name, ok := payload["name"].(string); ok && strings.TrimSpace(name) != "" {
		target.Name = name
	}
	if tType, ok := payload["type"].(string); ok && tType != "" {
		target.Type = models.TargetType(tType)
	}
	if addr, ok := payload["target"].(string); ok && strings.TrimSpace(addr) != "" {
		target.Target = addr
	}
	if interval, ok := payload["interval_ms"].(float64); ok && interval > 0 {
		target.IntervalMs = int(interval)
	}
	if timeout, ok := payload["timeout_ms"].(float64); ok && timeout > 0 {
		target.TimeoutMs = int(timeout)
	}
	if retries, ok := payload["retries"].(float64); ok && retries >= 0 {
		target.Retries = int(retries)
	}
	if enabled, ok := payload["enabled"].(bool); ok {
		target.Enabled = enabled
	}
	if labels, ok := payload["labels"].(map[string]any); ok {
		newLabels := make(map[string]string, len(labels))
		for k, v := range labels {
			newLabels[k] = fmt.Sprintf("%v", v)
		}
		target.Labels = newLabels
	}
	if cfg, ok := payload["config"].(map[string]any); ok {
		target.Config = cfg
	}
	target.UpdatedAt = time.Now().UTC()

	if err := target.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.meta.UpdateTarget(r.Context(), target); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "target not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, target)
}

// DeleteTargetHandler serves DELETE /api/v1/targets/{id}.
func (h *Handlers) DeleteTargetHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	if _, err := h.meta.GetTarget(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "target not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.meta.DeleteTarget(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "target not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "target deleted",
	})
}

// GetTargetStatusHandler serves GET /api/v1/targets/{id}/status.
func (h *Handlers) GetTargetStatusHandler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var target *models.Target
	if h.meta != nil {
		tgt, err := h.meta.GetTarget(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				respondError(w, http.StatusNotFound, "target not found")
				return
			}
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		target = tgt
	}

	if h.prober != nil {
		if res, ok := h.prober.GetLastResult(id); ok && res != nil {
			respondJSON(w, http.StatusOK, res)
			return
		}
	}

	// Target exists, but probe result has not yet been registered
	if target != nil {
		res := &models.ProbeResult{
			TargetID:   target.ID,
			TargetName: target.Name,
			TargetType: target.Type,
			Timestamp:  time.Now().UTC(),
			Success:    true,
			Metrics:    []models.MetricPoint{},
			Metadata:   map[string]any{"status": "initialized"},
		}
		respondJSON(w, http.StatusOK, res)
		return
	}

	respondError(w, http.StatusNotFound, "target not found")
}

// GetTargetMetricsHandler serves GET /api/v1/targets/{id}/metrics?metric=&start=&end=&step=.
func (h *Handlers) GetTargetMetricsHandler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.meta != nil {
		if _, err := h.meta.GetTarget(r.Context(), id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				respondError(w, http.StatusNotFound, "target not found")
				return
			}
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if h.tsdb == nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"metrics": []models.MetricPoint{},
		})
		return
	}

	metric := r.URL.Query().Get("metric")
	var start, end time.Time
	var step time.Duration

	if s := r.URL.Query().Get("start"); s != "" {
		if t, err := parseTime(s); err == nil {
			start = t
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		if t, err := parseTime(e); err == nil {
			end = t
		}
	}
	if st := r.URL.Query().Get("step"); st != "" {
		if d, err := time.ParseDuration(st); err == nil {
			step = d
		}
	}

	points, err := h.tsdb.QueryRange(id, metric, start, end, step)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if points == nil {
		points = []models.MetricPoint{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"metrics": points,
	})
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// Try parsing unix seconds or milliseconds
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e11 {
			return time.UnixMilli(n).UTC(), nil
		}
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Time{}, errors.New("unrecognized time format")
}

// ListAlertsHandler serves GET /api/v1/alerts.
func (h *Handlers) ListAlertsHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	status := r.URL.Query().Get("status")
	if status == "" {
		status = string(models.AlertStatusFiring)
	} else if status == "all" {
		status = ""
	}

	events, err := h.meta.ListAlertEvents(r.Context(), status, 100)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []*models.AlertEvent{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"alerts": events,
	})
}

// ListAlertRulesHandler serves GET /api/v1/alerts/rules.
func (h *Handlers) ListAlertRulesHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	var enabled *bool
	if enStr := r.URL.Query().Get("enabled"); enStr != "" {
		if b, err := strconv.ParseBool(enStr); err == nil {
			enabled = &b
		}
	}

	rules, err := h.meta.ListAlertRules(r.Context(), enabled)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []*models.AlertRule{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"rules": rules,
	})
}

// CreateAlertRuleHandler serves POST /api/v1/alerts/rules.
func (h *Handlers) CreateAlertRuleHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	var rule models.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid alert rule payload: %v", err))
		return
	}

	if rule.ID == "" {
		rule.ID = generateUUID()
	}
	now := time.Now().UTC()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now

	if err := rule.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.meta.CreateAlertRule(r.Context(), &rule); err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			respondError(w, http.StatusConflict, "alert rule already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, &rule)
}

// GetAlertRuleHandler serves GET /api/v1/alerts/rules/{id}.
func (h *Handlers) GetAlertRuleHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	rule, err := h.meta.GetAlertRule(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "alert rule not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, rule)
}

// UpdateAlertRuleHandler serves PUT /api/v1/alerts/rules/{id}.
func (h *Handlers) UpdateAlertRuleHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	if _, err := h.meta.GetAlertRule(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "alert rule not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var rule models.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid alert rule payload: %v", err))
		return
	}
	rule.ID = id
	rule.UpdatedAt = time.Now().UTC()

	if err := rule.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.meta.UpdateAlertRule(r.Context(), &rule); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "alert rule not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, &rule)
}

// DeleteAlertRuleHandler serves DELETE /api/v1/alerts/rules/{id}.
func (h *Handlers) DeleteAlertRuleHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	if _, err := h.meta.GetAlertRule(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "alert rule not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.meta.DeleteAlertRule(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "alert rule not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "alert rule deleted",
	})
}

// CreateSilenceHandler serves POST /api/v1/alerts/silence.
func (h *Handlers) CreateSilenceHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	var silence models.AlertSilence
	if err := json.NewDecoder(r.Body).Decode(&silence); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid silence payload: %v", err))
		return
	}

	if silence.ID == "" {
		silence.ID = generateUUID()
	}
	now := time.Now().UTC()
	if silence.CreatedAt.IsZero() {
		silence.CreatedAt = now
	}

	if err := silence.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.meta.CreateSilence(r.Context(), &silence); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, &silence)
}

// ListSilencesHandler serves GET /api/v1/alerts/silences.
func (h *Handlers) ListSilencesHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	silences, err := h.meta.ListActiveSilences(r.Context(), time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if silences == nil {
		silences = []*models.AlertSilence{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"silences": silences,
	})
}

// DeleteSilenceHandler serves DELETE /api/v1/alerts/silences/{id}.
func (h *Handlers) DeleteSilenceHandler(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		respondError(w, http.StatusInternalServerError, "metadata store not configured")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.meta.DeleteSilence(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			respondError(w, http.StatusNotFound, "silence not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "silence deleted",
	})
}

// GetConfigHandler serves GET /api/v1/config with secrets redacted.
func (h *Handlers) GetConfigHandler(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		respondJSON(w, http.StatusOK, map[string]any{})
		return
	}

	redacted := h.cfg.Redacted()
	respondJSON(w, http.StatusOK, redacted)
}

// ReloadConfigHandler serves PUT and POST /api/v1/config/reload.
func (h *Handlers) ReloadConfigHandler(w http.ResponseWriter, r *http.Request) {
	if h.reloadFunc != nil {
		if err := h.reloadFunc(r.Context()); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("reload failed: %v", err))
			return
		}
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "configuration reloaded",
	})
}
