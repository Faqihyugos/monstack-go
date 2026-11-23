package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace is the Prometheus metrics namespace prefix for MonStack-Go.
const Namespace = "monstack"

// Metrics encapsulates isolated Prometheus collectors for MonStack-Go.
type Metrics struct {
	reg *prometheus.Registry

	// Core Probe Collectors
	TargetsCount     *prometheus.GaugeVec     // monstack_targets_total{type, enabled}
	ProbeTotal       *prometheus.CounterVec   // monstack_probe_total{type, status}
	ProbeDuration    *prometheus.HistogramVec // monstack_probe_duration_seconds{type}
	ProbeErrorsTotal *prometheus.CounterVec   // monstack_probe_errors_total{type, reason}

	// Alert Collectors
	AlertsActive    *prometheus.GaugeVec   // monstack_alerts_active{severity}
	AlertsSentTotal *prometheus.CounterVec // monstack_alerts_sent_total{channel, status}

	// HTTP Server Collectors
	HTTPRequestsTotal   *prometheus.CounterVec   // monstack_http_requests_total{method, path, status}
	HTTPRequestDuration *prometheus.HistogramVec // monstack_http_request_duration_seconds{method, path}

	// TSDB Collectors
	TSDBSamplesWritten prometheus.Counter // monstack_tsdb_samples_written_total

	// System / Build
	BuildInfo *prometheus.GaugeVec // monstack_build_info{version, go_version}
}

// NewMetrics creates an isolated Metrics instance with a fresh registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		reg: reg,

		TargetsCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "targets_total",
			Help:      "Total number of configured targets by type and status.",
		}, []string{"type", "enabled"}),

		ProbeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "probe_total",
			Help:      "Total number of probes executed partitioned by target type and status.",
		}, []string{"type", "status"}),

		ProbeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "probe_duration_seconds",
			Help:      "Probe duration in seconds.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
		}, []string{"type"}),

		ProbeErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "probe_errors_total",
			Help:      "Total number of probe errors partitioned by target type and failure reason.",
		}, []string{"type", "reason"}),

		AlertsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "alerts_active",
			Help:      "Current count of active firing alerts by severity.",
		}, []string{"severity"}),

		AlertsSentTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "alerts_sent_total",
			Help:      "Total number of alert notifications sent by channel and status.",
		}, []string{"channel", "status"}),

		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "http_requests_total",
			Help:      "Total count of HTTP requests handled by method, path, and response status code.",
		}, []string{"method", "path", "status"}),

		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request latency histogram in seconds.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
		}, []string{"method", "path"}),

		TSDBSamplesWritten: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "tsdb_samples_written_total",
			Help:      "Total count of time series metric samples written to TSDB.",
		}),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "build_info",
			Help:      "Build version and Go version info constant.",
		}, []string{"version", "go_version"}),
	}

	reg.MustRegister(
		m.TargetsCount,
		m.ProbeTotal,
		m.ProbeDuration,
		m.ProbeErrorsTotal,
		m.AlertsActive,
		m.AlertsSentTotal,
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.TSDBSamplesWritten,
		m.BuildInfo,
	)

	return m
}

// Registry returns the underlying prometheus.Registry.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.reg
}

// Handler returns an http.Handler that serves Prometheus metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// RecordProbe records a probe execution with status and duration.
func (m *Metrics) RecordProbe(targetType string, success bool, duration time.Duration) {
	status := "failure"
	if success {
		status = "success"
	}
	m.ProbeTotal.WithLabelValues(targetType, status).Inc()
	m.ProbeDuration.WithLabelValues(targetType).Observe(duration.Seconds())
}

// RecordProbeError records a probe error event with a specific failure reason.
func (m *Metrics) RecordProbeError(targetType, reason string) {
	m.ProbeErrorsTotal.WithLabelValues(targetType, reason).Inc()
}

// SetTargetsCount updates the gauge for targets count by type and enabled state.
func (m *Metrics) SetTargetsCount(targetType string, enabled bool, count float64) {
	m.TargetsCount.WithLabelValues(targetType, strconv.FormatBool(enabled)).Set(count)
}

// RecordHTTPRequest records an incoming HTTP request's status and duration.
func (m *Metrics) RecordHTTPRequest(method, path string, status int, duration time.Duration) {
	m.HTTPRequestsTotal.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
	m.HTTPRequestDuration.WithLabelValues(method, path).Observe(duration.Seconds())
}

// SetActiveAlerts updates the active alerts count for a given severity.
func (m *Metrics) SetActiveAlerts(severity string, count float64) {
	m.AlertsActive.WithLabelValues(severity).Set(count)
}

// RecordAlertSent increments the count of alerts sent.
func (m *Metrics) RecordAlertSent(channel, status string) {
	m.AlertsSentTotal.WithLabelValues(channel, status).Inc()
}

// SetBuildInfo sets the build info metric.
func (m *Metrics) SetBuildInfo(version, goVersion string) {
	m.BuildInfo.WithLabelValues(version, goVersion).Set(1)
}

// Global default singleton metrics instance.
var defaultMetrics = NewMetrics()

// Default returns the default global Metrics instance.
func Default() *Metrics { return defaultMetrics }

// Handler returns the HTTP handler for the default global metrics registry.
func Handler() http.Handler { return defaultMetrics.Handler() }

// RecordProbe records a probe execution on the default global metrics.
func RecordProbe(targetType string, success bool, duration time.Duration) {
	defaultMetrics.RecordProbe(targetType, success, duration)
}

// RecordProbeError records a probe error on the default global metrics.
func RecordProbeError(targetType, reason string) {
	defaultMetrics.RecordProbeError(targetType, reason)
}

// SetTargetsCount updates the targets count on the default global metrics.
func SetTargetsCount(targetType string, enabled bool, count float64) {
	defaultMetrics.SetTargetsCount(targetType, enabled, count)
}

// RecordHTTPRequest records an HTTP request on the default global metrics.
func RecordHTTPRequest(method, path string, status int, duration time.Duration) {
	defaultMetrics.RecordHTTPRequest(method, path, status, duration)
}

// SetActiveAlerts updates the active alerts count on the default global metrics.
func SetActiveAlerts(severity string, count float64) {
	defaultMetrics.SetActiveAlerts(severity, count)
}

// RecordAlertSent increments the alert sent count on the default global metrics.
func RecordAlertSent(channel, status string) {
	defaultMetrics.RecordAlertSent(channel, status)
}

// SetBuildInfo sets the build info on the default global metrics.
func SetBuildInfo(version, goVersion string) {
	defaultMetrics.SetBuildInfo(version, goVersion)
}
