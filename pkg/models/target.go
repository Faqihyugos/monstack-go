package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// TargetType defines the probe protocol or mechanism.
type TargetType string

const (
	TargetTypeHTTP     TargetType = "http"
	TargetTypeICMP     TargetType = "icmp"
	TargetTypeTCP      TargetType = "tcp"
	TargetTypeUDP      TargetType = "udp"
	TargetTypeDNS      TargetType = "dns"
	TargetTypeSNMP     TargetType = "snmp"
	TargetTypeGRPC     TargetType = "grpc"
	TargetTypeTLS      TargetType = "tls"
	TargetTypePortScan TargetType = "port_scan"
)

// IsValid checks if the TargetType is a recognized type.
func (t TargetType) IsValid() bool {
	switch t {
	case TargetTypeHTTP, TargetTypeICMP, TargetTypeTCP, TargetTypeUDP,
		TargetTypeDNS, TargetTypeSNMP, TargetTypeGRPC, TargetTypeTLS, TargetTypePortScan:
		return true
	default:
		return false
	}
}

// Target represents a monitored target endpoint.
type Target struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       TargetType        `json:"type"`
	Target     string            `json:"target"`
	IntervalMs int               `json:"interval_ms"`
	TimeoutMs  int               `json:"timeout_ms"`
	Retries    int               `json:"retries"`
	Config     map[string]any    `json:"config"`
	Labels     map[string]string `json:"labels"`
	Enabled    bool              `json:"enabled"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// HTTPConfig defines typed parameters for HTTP/HTTPS prober.
type HTTPConfig struct {
	Method          string            `json:"method"`
	ExpectedStatus  []int             `json:"expected_status"`
	Headers         map[string]string `json:"headers,omitempty"`
	BodyMatch       string            `json:"body_match,omitempty"`
	FollowRedirects bool              `json:"follow_redirects"`
	MaxRedirects    int               `json:"max_redirects"`
	TLSSkipVerify   bool              `json:"tls_skip_verify"`
	TLSMinVersion   string            `json:"tls_min_version,omitempty"`
}

// ICMPConfig defines typed parameters for ICMP prober.
type ICMPConfig struct {
	Count      int    `json:"count"`
	PacketSize int    `json:"packet_size"`
	Protocol   string `json:"protocol"` // "ip4" or "ip6"
}

// TCPConfig defines typed parameters for TCP prober.
type TCPConfig struct {
	SendPayload string `json:"send_payload,omitempty"`
	ExpectMatch string `json:"expect_match,omitempty"`
	TLS         bool   `json:"tls,omitempty"`
}

// NewTarget creates a new Target instance with default values and timestamps.
func NewTarget(name string, tType TargetType, targetAddr string, intervalMs, timeoutMs int) *Target {
	now := time.Now().UTC()
	if intervalMs <= 0 {
		intervalMs = 30000
	}
	if timeoutMs <= 0 {
		timeoutMs = 10000
	}
	return &Target{
		Name:       name,
		Type:       tType,
		Target:     targetAddr,
		IntervalMs: intervalMs,
		TimeoutMs:  timeoutMs,
		Retries:    2,
		Config:     make(map[string]any),
		Labels:     make(map[string]string),
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// Validate verifies structural constraints and valid address formatting.
func (t *Target) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("target name cannot be empty")
	}
	if len(t.Name) > 128 {
		return errors.New("target name exceeds 128 characters")
	}
	if !t.Type.IsValid() {
		return fmt.Errorf("invalid target type: %s", t.Type)
	}
	if strings.TrimSpace(t.Target) == "" {
		return errors.New("target address cannot be empty")
	}
	if t.IntervalMs <= 0 {
		return errors.New("interval_ms must be greater than 0")
	}
	if t.TimeoutMs <= 0 {
		return errors.New("timeout_ms must be greater than 0")
	}
	if t.TimeoutMs > t.IntervalMs {
		return errors.New("timeout_ms cannot exceed interval_ms")
	}
	if t.Retries < 0 {
		return errors.New("retries cannot be negative")
	}

	// Address format validation per type
	switch t.Type {
	case TargetTypeHTTP:
		u, err := url.ParseRequestURI(t.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.TrimSpace(u.Host) == "" {
			return fmt.Errorf("invalid HTTP target URL: %s", t.Target)
		}
	case TargetTypeTCP:
		host, port, err := net.SplitHostPort(t.Target)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("invalid TCP target, expected host:port, got: %s", t.Target)
		}
	case TargetTypeICMP:
		if strings.HasPrefix(t.Target, "http://") || strings.HasPrefix(t.Target, "https://") {
			return fmt.Errorf("ICMP target must not have a URL scheme: %s", t.Target)
		}
	}
	return nil
}

// GetHTTPConfig extracts and parses HTTP-specific configuration with defaults.
func (t *Target) GetHTTPConfig() (*HTTPConfig, error) {
	cfg := &HTTPConfig{
		Method:          "GET",
		ExpectedStatus:  []int{200},
		FollowRedirects: true,
		MaxRedirects:    3,
	}
	if t.Config == nil || len(t.Config) == 0 {
		return cfg, nil
	}
	data, err := json.Marshal(t.Config)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if len(cfg.ExpectedStatus) == 0 {
		cfg.ExpectedStatus = []int{200}
	}
	if cfg.Method == "" {
		cfg.Method = "GET"
	}
	return cfg, nil
}

// GetICMPConfig extracts and parses ICMP-specific configuration with defaults.
func (t *Target) GetICMPConfig() (*ICMPConfig, error) {
	cfg := &ICMPConfig{
		Count:      3,
		PacketSize: 64,
		Protocol:   "ip4",
	}
	if t.Config == nil || len(t.Config) == 0 {
		return cfg, nil
	}
	data, err := json.Marshal(t.Config)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Count <= 0 {
		cfg.Count = 3
	}
	if cfg.PacketSize <= 0 {
		cfg.PacketSize = 64
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "ip4"
	}
	return cfg, nil
}

// GetTCPConfig extracts and parses TCP-specific configuration with defaults.
func (t *Target) GetTCPConfig() (*TCPConfig, error) {
	cfg := &TCPConfig{}
	if t.Config == nil || len(t.Config) == 0 {
		return cfg, nil
	}
	data, err := json.Marshal(t.Config)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
