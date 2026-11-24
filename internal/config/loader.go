package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

// ErrConfigNotFound is returned when configuration file cannot be found.
var ErrConfigNotFound = errors.New("configuration file not found")

// Load reads and parses configuration from path, applying defaults, env overrides, and validation.
func Load(path string) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetEnvPrefix("MONSTACK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		v.SetConfigFile(path)
	} else {
		// Search default locations
		v.SetConfigName("monstack")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs/examples")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/monstack")
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) && path == "" {
			// No config file found in default paths, proceed with defaults
		} else {
			return nil, fmt.Errorf("failed to read configuration: %w", err)
		}
	}

	var cfg Config
	decodeHook := viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	))

	if err := v.Unmarshal(&cfg, decodeHook); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	applyTargetDefaults(&cfg)

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return &cfg, nil
}

// setDefaults configures Viper default values.
func setDefaults(v *viper.Viper) {
	// Server defaults
	v.SetDefault("server.listen", ":8443")
	v.SetDefault("server.mode", "https")
	v.SetDefault("server.read_timeout", "30s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.tls.mode", "self-signed")

	// Auth defaults
	v.SetDefault("auth.enabled", true)
	v.SetDefault("auth.admin_user", "admin")
	v.SetDefault("auth.admin_password", "changeme")
	v.SetDefault("auth.jwt.expiry", "24h")

	// Storage defaults
	v.SetDefault("storage.tsdb.path", "/var/lib/monstack/data")
	v.SetDefault("storage.tsdb.retention", "30d")
	v.SetDefault("storage.tsdb.max_disk", "50GB")
	v.SetDefault("storage.metadata.path", "/var/lib/monstack/meta.db")
	v.SetDefault("storage.metadata.wal_mode", true)
	v.SetDefault("storage.metadata.busy_timeout", "5s")
	v.SetDefault("storage.cache.enabled", true)
	v.SetDefault("storage.cache.max_memory", "256MB")
	v.SetDefault("storage.cache.ttl", "5m")

	// GRPC defaults
	v.SetDefault("grpc.listen", ":9443")
	v.SetDefault("grpc.tls", true)
	v.SetDefault("grpc.max_recv_msg_size", "16MB")

	// Probes defaults
	v.SetDefault("probes.defaults.interval", "30s")
	v.SetDefault("probes.defaults.timeout", "10s")
	v.SetDefault("probes.defaults.retries", 2)

	// Alerting defaults
	v.SetDefault("alerting.evaluation_interval", "10s")
	v.SetDefault("alerting.routing.default_channel", "default-webhook")

	// Logging defaults
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output", "stdout")
}

// applyTargetDefaults sets default probe interval/timeout/retries if omitted in individual targets.
func applyTargetDefaults(cfg *Config) {
	defaultInterval := cfg.Probes.Defaults.Interval
	if defaultInterval <= 0 {
		defaultInterval = 30 * time.Second
	}
	defaultTimeout := cfg.Probes.Defaults.Timeout
	if defaultTimeout <= 0 {
		defaultTimeout = 10 * time.Second
	}
	defaultRetries := cfg.Probes.Defaults.Retries
	if defaultRetries <= 0 {
		defaultRetries = 2
	}

	if cfg.Probes.Defaults.Interval == 0 {
		cfg.Probes.Defaults.Interval = defaultInterval
	}
	if cfg.Probes.Defaults.Timeout == 0 {
		cfg.Probes.Defaults.Timeout = defaultTimeout
	}
	if cfg.Probes.Defaults.Retries == 0 {
		cfg.Probes.Defaults.Retries = defaultRetries
	}

	for i := range cfg.Probes.Targets {
		t := &cfg.Probes.Targets[i]
		if t.Interval == 0 && t.IntervalMs > 0 {
			t.Interval = time.Duration(t.IntervalMs) * time.Millisecond
		}
		if t.Timeout == 0 && t.TimeoutMs > 0 {
			t.Timeout = time.Duration(t.TimeoutMs) * time.Millisecond
			if t.Interval > 0 && t.Timeout > t.Interval {
				t.Timeout = t.Interval
			}
		}
		if t.Interval == 0 {
			t.Interval = defaultInterval
		}
		if t.Timeout == 0 {
			t.Timeout = defaultTimeout
		}
		if t.Retries == 0 {
			t.Retries = defaultRetries
		}
		if t.Enabled == nil {
			enabled := true
			t.Enabled = &enabled
		}
	}
}

// Validate performs structural and semantic checks on Config.
func Validate(cfg *Config) error {
	var errs []string

	// Validate Server
	if cfg.Server.Listen == "" {
		errs = append(errs, "server.listen is required")
	} else if _, _, err := net.SplitHostPort(cfg.Server.Listen); err != nil && !strings.HasPrefix(cfg.Server.Listen, ":") {
		errs = append(errs, fmt.Sprintf("server.listen %q is not a valid address", cfg.Server.Listen))
	}

	switch cfg.Server.Mode {
	case "http", "https", "auto":
	default:
		errs = append(errs, fmt.Sprintf("invalid server.mode %q: must be http, https, or auto", cfg.Server.Mode))
	}

	if cfg.Server.Mode == "https" {
		switch cfg.Server.TLS.Mode {
		case "self-signed":
		case "acme":
			if cfg.Server.TLS.ACME.Email == "" {
				errs = append(errs, "server.tls.acme.email is required when tls.mode is acme")
			}
			if len(cfg.Server.TLS.ACME.Domains) == 0 {
				errs = append(errs, "server.tls.acme.domains must have at least one domain")
			}
		case "manual":
			if cfg.Server.TLS.Manual.CertFile == "" || cfg.Server.TLS.Manual.KeyFile == "" {
				errs = append(errs, "server.tls.manual cert_file and key_file are required when tls.mode is manual")
			}
		default:
			errs = append(errs, fmt.Sprintf("invalid server.tls.mode %q: must be self-signed, acme, or manual", cfg.Server.TLS.Mode))
		}
	}

	// Validate Storage
	if cfg.Storage.TSDB.Path == "" {
		errs = append(errs, "storage.tsdb.path is required")
	}
	if ret, err := cfg.Storage.TSDB.RetentionDuration(); err != nil {
		errs = append(errs, fmt.Sprintf("invalid storage.tsdb.retention %q: %v", cfg.Storage.TSDB.Retention, err))
	} else if ret <= 0 {
		errs = append(errs, fmt.Sprintf("storage.tsdb.retention must be greater than zero, got %s", cfg.Storage.TSDB.Retention))
	}
	if maxDisk, err := cfg.Storage.TSDB.MaxDiskBytes(); err != nil {
		errs = append(errs, fmt.Sprintf("invalid storage.tsdb.max_disk %q: %v", cfg.Storage.TSDB.MaxDisk, err))
	} else if maxDisk <= 0 {
		errs = append(errs, fmt.Sprintf("storage.tsdb.max_disk must be greater than zero, got %s", cfg.Storage.TSDB.MaxDisk))
	}
	if cfg.Storage.Metadata.Path == "" {
		errs = append(errs, "storage.metadata.path is required")
	}

	// Validate Logging
	switch strings.ToLower(cfg.Logging.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		errs = append(errs, fmt.Sprintf("invalid logging.level %q: must be debug, info, warn, or error", cfg.Logging.Level))
	}

	switch strings.ToLower(cfg.Logging.Format) {
	case "json", "text":
	default:
		errs = append(errs, fmt.Sprintf("invalid logging.format %q: must be json or text", cfg.Logging.Format))
	}

	// Validate Probe Defaults
	if cfg.Probes.Defaults.Interval <= 0 {
		errs = append(errs, "probes.defaults.interval must be greater than zero")
	}
	if cfg.Probes.Defaults.Timeout <= 0 {
		errs = append(errs, "probes.defaults.timeout must be greater than zero")
	}
	if cfg.Probes.Defaults.Timeout > cfg.Probes.Defaults.Interval && cfg.Probes.Defaults.Interval > 0 {
		errs = append(errs, "probes.defaults.timeout cannot be greater than interval")
	}
	if cfg.Probes.Defaults.Retries < 0 {
		errs = append(errs, "probes.defaults.retries must be non-negative")
	}

	// Validate Targets
	targetNames := make(map[string]struct{})
	for i, t := range cfg.Probes.Targets {
		if t.Name == "" {
			errs = append(errs, fmt.Sprintf("probes.targets[%d].name is required", i))
		} else {
			if _, exists := targetNames[t.Name]; exists {
				errs = append(errs, fmt.Sprintf("duplicate target name %q in probes.targets", t.Name))
			}
			targetNames[t.Name] = struct{}{}
		}
		switch t.Type {
		case "http", "icmp", "tcp":
		default:
			errs = append(errs, fmt.Sprintf("probes.targets[%d].type %q is unsupported; must be http, icmp, or tcp", i, t.Type))
		}
		if t.Target == "" {
			errs = append(errs, fmt.Sprintf("probes.targets[%d].target is required", i))
		}
		if t.Interval <= 0 {
			errs = append(errs, fmt.Sprintf("probes.targets[%d].interval must be greater than zero", i))
		}
		if t.Timeout <= 0 {
			errs = append(errs, fmt.Sprintf("probes.targets[%d].timeout must be greater than zero", i))
		}
		if t.Timeout > t.Interval && t.Interval > 0 {
			errs = append(errs, fmt.Sprintf("probes.targets[%d].timeout cannot be greater than interval", i))
		}
		if t.Retries < 0 {
			errs = append(errs, fmt.Sprintf("probes.targets[%d].retries must be non-negative", i))
		}
	}

	// Validate Alerting
	if cfg.Alerting.EvaluationInterval <= 0 {
		errs = append(errs, "alerting.evaluation_interval must be greater than zero")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
