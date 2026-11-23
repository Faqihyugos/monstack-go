package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config represents the complete MonStack-Go server configuration.
type Config struct {
	Server    ServerConfig    `mapstructure:"server" yaml:"server" json:"server"`
	Auth      AuthConfig      `mapstructure:"auth" yaml:"auth" json:"auth"`
	Storage   StorageConfig   `mapstructure:"storage" yaml:"storage" json:"storage"`
	GRPC      GRPCConfig      `mapstructure:"grpc" yaml:"grpc" json:"grpc"`
	Probes    ProbesConfig    `mapstructure:"probes" yaml:"probes" json:"probes"`
	Alerting  AlertingConfig  `mapstructure:"alerting" yaml:"alerting" json:"alerting"`
	Discovery []DiscoveryRule `mapstructure:"discovery" yaml:"discovery" json:"discovery"`
	Logging   LoggingConfig   `mapstructure:"logging" yaml:"logging" json:"logging"`
	Telemetry TelemetryConfig `mapstructure:"telemetry" yaml:"telemetry" json:"telemetry"`
}

// ServerConfig defines HTTP/HTTPS server parameters.
type ServerConfig struct {
	Listen       string        `mapstructure:"listen" yaml:"listen" json:"listen"`
	Mode         string        `mapstructure:"mode" yaml:"mode" json:"mode"` // "http", "https", "auto"
	ReadTimeout  time.Duration `mapstructure:"read_timeout" yaml:"read_timeout" json:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout" yaml:"write_timeout" json:"write_timeout"`
	TLS          TLSConfig     `mapstructure:"tls" yaml:"tls" json:"tls"`
}

// TLSConfig defines TLS mode and credentials.
type TLSConfig struct {
	Mode   string     `mapstructure:"mode" yaml:"mode" json:"mode"` // "self-signed", "acme", "manual"
	ACME   ACMEConfig `mapstructure:"acme" yaml:"acme" json:"acme"`
	Manual ManualTLS  `mapstructure:"manual" yaml:"manual" json:"manual"`
}

// ACMEConfig defines Let's Encrypt automated certificate generation.
type ACMEConfig struct {
	Email    string   `mapstructure:"email" yaml:"email" json:"email"`
	Domains  []string `mapstructure:"domains" yaml:"domains" json:"domains"`
	CacheDir string   `mapstructure:"cache_dir" yaml:"cache_dir" json:"cache_dir"`
}

// ManualTLS defines paths to custom TLS certificates.
type ManualTLS struct {
	CertFile string `mapstructure:"cert_file" yaml:"cert_file" json:"cert_file"`
	KeyFile  string `mapstructure:"key_file" yaml:"key_file" json:"key_file"`
}

// AuthConfig defines administrative authentication and JWT settings.
type AuthConfig struct {
	Enabled           bool           `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	AdminUser         string         `mapstructure:"admin_user" yaml:"admin_user" json:"admin_user"`
	AdminPassword     string         `mapstructure:"admin_password" yaml:"admin_password" json:"admin_password"`
	AdminPasswordHash string         `mapstructure:"admin_password_hash" yaml:"admin_password_hash" json:"admin_password_hash"`
	APIKeys           []APIKeyConfig `mapstructure:"api_keys" yaml:"api_keys" json:"api_keys"`
	JWT               JWTConfig      `mapstructure:"jwt" yaml:"jwt" json:"jwt"`
}

// APIKeyConfig defines static API keys with granular permissions.
type APIKeyConfig struct {
	Name        string   `mapstructure:"name" yaml:"name" json:"name"`
	Key         string   `mapstructure:"key" yaml:"key" json:"key"`
	Permissions []string `mapstructure:"permissions" yaml:"permissions" json:"permissions"`
}

// JWTConfig defines token signing and expiration parameters.
type JWTConfig struct {
	Secret string        `mapstructure:"secret" yaml:"secret" json:"secret"`
	Expiry time.Duration `mapstructure:"expiry" yaml:"expiry" json:"expiry"`
}

// StorageConfig defines TSDB, metadata SQLite, and cache configurations.
type StorageConfig struct {
	TSDB     TSDBConfig     `mapstructure:"tsdb" yaml:"tsdb" json:"tsdb"`
	Metadata MetadataConfig `mapstructure:"metadata" yaml:"metadata" json:"metadata"`
	Cache    CacheConfig    `mapstructure:"cache" yaml:"cache" json:"cache"`
}

// TSDBConfig defines time-series database retention and disk quotas.
type TSDBConfig struct {
	Path          string        `mapstructure:"path" yaml:"path" json:"path"`
	Retention     string        `mapstructure:"retention" yaml:"retention" json:"retention"` // e.g. "30d"
	MaxDisk       string        `mapstructure:"max_disk" yaml:"max_disk" json:"max_disk"`   // e.g. "50GB"
	BlockDuration time.Duration `mapstructure:"block_duration" yaml:"block_duration" json:"block_duration"`
}

// RetentionDuration parses retention string, supporting days ('d'), hours ('h'), minutes ('m').
func (c *TSDBConfig) RetentionDuration() (time.Duration, error) {
	return ParseDuration(c.Retention)
}

// MaxDiskBytes parses MaxDisk string into byte count.
func (c *TSDBConfig) MaxDiskBytes() (int64, error) {
	return ParseBytes(c.MaxDisk)
}

// MetadataConfig defines SQLite metadata path and connection options.
type MetadataConfig struct {
	Path        string        `mapstructure:"path" yaml:"path" json:"path"`
	WALMode     bool          `mapstructure:"wal_mode" yaml:"wal_mode" json:"wal_mode"`
	BusyTimeout time.Duration `mapstructure:"busy_timeout" yaml:"busy_timeout" json:"busy_timeout"`
}

// CacheConfig defines optional in-memory caching options.
type CacheConfig struct {
	Enabled   bool          `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	MaxMemory string        `mapstructure:"max_memory" yaml:"max_memory" json:"max_memory"`
	TTL       time.Duration `mapstructure:"ttl" yaml:"ttl" json:"ttl"`
}

// GRPCConfig defines internal gRPC agent communications listener.
type GRPCConfig struct {
	Listen         string `mapstructure:"listen" yaml:"listen" json:"listen"`
	TLS            bool   `mapstructure:"tls" yaml:"tls" json:"tls"`
	MaxRecvMsgSize string `mapstructure:"max_recv_msg_size" yaml:"max_recv_msg_size" json:"max_recv_msg_size"`
}

// ProbesConfig defines defaults and target definitions.
type ProbesConfig struct {
	Defaults ProbeDefaultsConfig `mapstructure:"defaults" yaml:"defaults" json:"defaults"`
	Targets  []ProbeTargetConfig `mapstructure:"targets" yaml:"targets" json:"targets"`
}

// ProbeDefaultsConfig defines fallback intervals, timeouts, and retries.
type ProbeDefaultsConfig struct {
	Interval time.Duration `mapstructure:"interval" yaml:"interval" json:"interval"`
	Timeout  time.Duration `mapstructure:"timeout" yaml:"timeout" json:"timeout"`
	Retries  int           `mapstructure:"retries" yaml:"retries" json:"retries"`
}

// ProbeTargetConfig defines a specific monitored target.
type ProbeTargetConfig struct {
	Name     string            `mapstructure:"name" yaml:"name" json:"name"`
	Type     string            `mapstructure:"type" yaml:"type" json:"type"` // "http", "icmp", "tcp"
	Target   string            `mapstructure:"target" yaml:"target" json:"target"`
	Interval time.Duration     `mapstructure:"interval" yaml:"interval" json:"interval"`
	Timeout  time.Duration     `mapstructure:"timeout" yaml:"timeout" json:"timeout"`
	Retries  int               `mapstructure:"retries" yaml:"retries" json:"retries"`
	Enabled  *bool             `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	Labels   map[string]string `mapstructure:"labels" yaml:"labels" json:"labels"`
	HTTP     *HTTPProbeConfig  `mapstructure:"http" yaml:"http,omitempty" json:"http,omitempty"`
	ICMP     *ICMPProbeConfig  `mapstructure:"icmp" yaml:"icmp,omitempty" json:"icmp,omitempty"`
	TCP      *TCPProbeConfig   `mapstructure:"tcp" yaml:"tcp,omitempty" json:"tcp,omitempty"`
}

// HTTPProbeConfig defines HTTP/HTTPS probe options.
type HTTPProbeConfig struct {
	Method          string            `mapstructure:"method" yaml:"method" json:"method"`
	ExpectedStatus  []int             `mapstructure:"expected_status" yaml:"expected_status" json:"expected_status"`
	Headers         map[string]string `mapstructure:"headers" yaml:"headers" json:"headers"`
	BodyMatch       string            `mapstructure:"body_match" yaml:"body_match" json:"body_match"`
	FollowRedirects bool              `mapstructure:"follow_redirects" yaml:"follow_redirects" json:"follow_redirects"`
	MaxRedirects    int               `mapstructure:"max_redirects" yaml:"max_redirects" json:"max_redirects"`
	TLS             *ProbeTLSConfig   `mapstructure:"tls" yaml:"tls,omitempty" json:"tls,omitempty"`
}

// ProbeTLSConfig defines TLS verification options for HTTP prober.
type ProbeTLSConfig struct {
	SkipVerify bool   `mapstructure:"skip_verify" yaml:"skip_verify" json:"skip_verify"`
	MinVersion string `mapstructure:"min_version" yaml:"min_version" json:"min_version"`
}

// ICMPProbeConfig defines ICMP ping options.
type ICMPProbeConfig struct {
	Count      int    `mapstructure:"count" yaml:"count" json:"count"`
	PacketSize int    `mapstructure:"packet_size" yaml:"packet_size" json:"packet_size"`
	Protocol   string `mapstructure:"protocol" yaml:"protocol" json:"protocol"`
}

// TCPProbeConfig defines TCP connect options.
type TCPProbeConfig struct {
	Send   string `mapstructure:"send" yaml:"send" json:"send"`
	Expect string `mapstructure:"expect" yaml:"expect" json:"expect"`
	TLS    bool   `mapstructure:"tls" yaml:"tls" json:"tls"`
}

// AlertingConfig defines evaluation interval, rules, channels, and routing.
type AlertingConfig struct {
	EvaluationInterval time.Duration        `mapstructure:"evaluation_interval" yaml:"evaluation_interval" json:"evaluation_interval"`
	RulesFile          string               `mapstructure:"rules_file" yaml:"rules_file" json:"rules_file"`
	Rules              []AlertRuleConfig    `mapstructure:"rules" yaml:"rules" json:"rules"`
	Channels           []AlertChannelConfig `mapstructure:"channels" yaml:"channels" json:"channels"`
	Routing            AlertRoutingConfig   `mapstructure:"routing" yaml:"routing" json:"routing"`
}

// AlertRuleConfig defines metric threshold rules.
type AlertRuleConfig struct {
	Name        string            `mapstructure:"name" yaml:"name" json:"name"`
	Expr        string            `mapstructure:"expr" yaml:"expr" json:"expr"`
	For         string            `mapstructure:"for" yaml:"for" json:"for"`
	Severity    string            `mapstructure:"severity" yaml:"severity" json:"severity"`
	Labels      map[string]string `mapstructure:"labels" yaml:"labels" json:"labels"`
	Annotations map[string]string `mapstructure:"annotations" yaml:"annotations" json:"annotations"`
	Channels    []string          `mapstructure:"channels" yaml:"channels" json:"channels"`
}

// AlertChannelConfig defines notification endpoints (telegram, webhook, slack).
type AlertChannelConfig struct {
	Name   string         `mapstructure:"name" yaml:"name" json:"name"`
	Type   string         `mapstructure:"type" yaml:"type" json:"type"`
	Config map[string]any `mapstructure:"config" yaml:"config" json:"config"`
}

// AlertRoutingConfig defines routing rules and default channel.
type AlertRoutingConfig struct {
	DefaultChannel string           `mapstructure:"default_channel" yaml:"default_channel" json:"default_channel"`
	Routes         []AlertRouteRule `mapstructure:"routes" yaml:"routes" json:"routes"`
}

// AlertRouteRule maps labels to target notification channels.
type AlertRouteRule struct {
	Match          map[string]string `mapstructure:"match" yaml:"match" json:"match"`
	Channels       []string          `mapstructure:"channels" yaml:"channels" json:"channels"`
	RepeatInterval time.Duration     `mapstructure:"repeat_interval" yaml:"repeat_interval" json:"repeat_interval"`
}

// DiscoveryRule defines service discovery sources.
type DiscoveryRule struct {
	Type   string         `mapstructure:"type" yaml:"type" json:"type"`
	Config map[string]any `mapstructure:"config" yaml:"config" json:"config"`
}

// LoggingConfig defines structured slog configuration.
type LoggingConfig struct {
	Level  string        `mapstructure:"level" yaml:"level" json:"level"`
	Format string        `mapstructure:"format" yaml:"format" json:"format"`
	Output string        `mapstructure:"output" yaml:"output" json:"output"`
	File   LogFileConfig `mapstructure:"file" yaml:"file" json:"file"`
}

// LogFileConfig defines file rotation options for log output.
type LogFileConfig struct {
	Path       string `mapstructure:"path" yaml:"path" json:"path"`
	MaxSize    string `mapstructure:"max_size" yaml:"max_size" json:"max_size"`
	MaxBackups int    `mapstructure:"max_backups" yaml:"max_backups" json:"max_backups"`
	Compress   bool   `mapstructure:"compress" yaml:"compress" json:"compress"`
}

// TelemetryConfig defines OpenTelemetry collector integration.
type TelemetryConfig struct {
	Enabled      bool   `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	OTLPEndpoint string `mapstructure:"otlp_endpoint" yaml:"otlp_endpoint" json:"otlp_endpoint"`
}

// ParseDuration handles standard durations plus 'd' (days).
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.ParseFloat(daysStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid day duration %q: %w", s, err)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

// ParseBytes parses human-readable byte sizes (e.g. 50GB, 256MB, 16KB).
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	var multiplier int64 = 1
	valStr := s
	switch {
	case strings.HasSuffix(s, "TB"):
		multiplier = 1024 * 1024 * 1024 * 1024
		valStr = strings.TrimSuffix(s, "TB")
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		valStr = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		valStr = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		valStr = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		multiplier = 1
		valStr = strings.TrimSuffix(s, "B")
	}
	val, err := strconv.ParseInt(strings.TrimSpace(valStr), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size %q: %w", s, err)
	}
	return val * multiplier, nil
}
