package models

import (
	"errors"
	"time"
)

// Standard metric names
const (
	MetricProbeSuccess            = "probe_success"
	MetricProbeDurationSeconds    = "probe_duration_seconds"
	MetricProbeResponseTime       = "probe_response_time"
	MetricHTTPStatusCode          = "probe_http_status_code"
	MetricHTTPDNSLookupSeconds    = "probe_dns_lookup_time_seconds"
	MetricHTTPTLSHandshakeSeconds = "probe_tls_handshake_time_seconds"
	MetricHTTPSSLExpiryDays       = "probe_ssl_days_remaining"
	MetricHTTPContentMatch        = "probe_content_match"
	MetricICMPRTTAverage          = "probe_icmp_rtt_avg_ms"
	MetricICMPRTTMin              = "probe_icmp_rtt_min_ms"
	MetricICMPRTTMax              = "probe_icmp_rtt_max_ms"
	MetricICMPPacketLoss          = "probe_icmp_packet_loss_percent"
	MetricICMPJitter              = "probe_icmp_jitter_ms"
	MetricTCPConnectSeconds       = "probe_tcp_connect_time_seconds"
	MetricTCPBannerMatch          = "probe_tcp_banner_match"
)

// MetricPoint represents a single data point in TSDB.
type MetricPoint struct {
	TargetID  string            `json:"target_id"`
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp time.Time         `json:"timestamp"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// Validate checks whether the MetricPoint is structurally valid.
func (m *MetricPoint) Validate() error {
	if m.TargetID == "" {
		return errors.New("metric point target_id cannot be empty")
	}
	if m.Metric == "" {
		return errors.New("metric point metric name cannot be empty")
	}
	if m.Timestamp.IsZero() {
		return errors.New("metric point timestamp cannot be zero")
	}
	return nil
}

// ProbeResult represents the outcome of an executed target probe.
type ProbeResult struct {
	TargetID     string         `json:"target_id"`
	TargetName   string         `json:"target_name"`
	TargetType   TargetType     `json:"target_type"`
	Success      bool           `json:"success"`
	ResponseTime time.Duration  `json:"response_time"`
	Error        string         `json:"error,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
	Metrics      []MetricPoint  `json:"metrics"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Validate checks whether the ProbeResult is structurally valid.
func (r *ProbeResult) Validate() error {
	if r.TargetID == "" {
		return errors.New("probe result target_id cannot be empty")
	}
	if !r.TargetType.IsValid() {
		return errors.New("invalid probe result target_type")
	}
	if r.Timestamp.IsZero() {
		return errors.New("probe result timestamp cannot be zero")
	}
	return nil
}

// ToMetricPoints converts ProbeResult into a slice of MetricPoints for TSDB storage.
func (r *ProbeResult) ToMetricPoints() []MetricPoint {
	var points []MetricPoint
	successVal := 0.0
	if r.Success {
		successVal = 1.0
	}
	points = append(points, MetricPoint{
		TargetID:  r.TargetID,
		Metric:    MetricProbeSuccess,
		Value:     successVal,
		Timestamp: r.Timestamp,
	})
	points = append(points, MetricPoint{
		TargetID:  r.TargetID,
		Metric:    MetricProbeDurationSeconds,
		Value:     r.ResponseTime.Seconds(),
		Timestamp: r.Timestamp,
	})
	// Include all auxiliary metrics collected during probe
	for _, m := range r.Metrics {
		cp := m
		if cp.TargetID == "" {
			cp.TargetID = r.TargetID
		}
		if cp.Timestamp.IsZero() {
			cp.Timestamp = r.Timestamp
		}
		points = append(points, cp)
	}
	return points
}
