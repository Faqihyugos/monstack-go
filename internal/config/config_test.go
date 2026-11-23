package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleValidConfig = `
server:
  listen: ":8443"
  mode: https
  read_timeout: 30s
  write_timeout: 30s
  tls:
    mode: self-signed

auth:
  enabled: true
  admin_user: "admin"
  admin_password: "supersecretpassword"
  admin_password_hash: "$2a$10$hashedsecretpassword"
  api_keys:
    - name: "ci"
      key: "secret-ci-key-12345"
      permissions: ["read", "write"]
  jwt:
    secret: "myjwtsecretkey123"
    expiry: 24h

storage:
  tsdb:
    path: "/tmp/monstack_test/data"
    retention: 30d
    max_disk: 50GB
  metadata:
    path: "/tmp/monstack_test/meta.db"
  cache:
    enabled: true
    max_memory: 256MB

grpc:
  listen: ":9443"
  tls: true
  max_recv_msg_size: 16MB

probes:
  defaults:
    interval: 30s
    timeout: 10s
    retries: 2
  targets:
    - name: "example-http"
      type: http
      target: "https://example.com"
      interval: 15s
      timeout: 5s
      http:
        method: GET
        expected_status: [200]
        headers:
          Authorization: "Bearer secret-token-xyz"
          Content-Type: "application/json"
      labels:
        environment: test

alerting:
  evaluation_interval: 10s
  rules:
    - name: "Target Down"
      expr: "probe_success == 0"
      for: 2m
      severity: critical
  channels:
    - name: "telegram-alerts"
      type: telegram
      config:
        bot_token: "123456:secret-bot-token"
        chat_id: "-100123"
  routing:
    default_channel: "telegram-alerts"

logging:
  level: debug
  format: json
  output: stdout
`

// Test 1: Loader verifies parsing of all fields
func TestLoad_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "monstack.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, ":8443", cfg.Server.Listen)
	assert.Equal(t, "https", cfg.Server.Mode)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, "self-signed", cfg.Server.TLS.Mode)

	assert.True(t, cfg.Auth.Enabled)
	assert.Equal(t, "admin", cfg.Auth.AdminUser)
	assert.Equal(t, "supersecretpassword", cfg.Auth.AdminPassword)
	assert.Equal(t, "myjwtsecretkey123", cfg.Auth.JWT.Secret)
	require.Len(t, cfg.Auth.APIKeys, 1)
	assert.Equal(t, "secret-ci-key-12345", cfg.Auth.APIKeys[0].Key)

	assert.Equal(t, "30d", cfg.Storage.TSDB.Retention)
	retDur, err := cfg.Storage.TSDB.RetentionDuration()
	require.NoError(t, err)
	assert.Equal(t, 30*24*time.Hour, retDur)

	diskBytes, err := cfg.Storage.TSDB.MaxDiskBytes()
	require.NoError(t, err)
	assert.Equal(t, int64(50*1024*1024*1024), diskBytes)

	require.Len(t, cfg.Probes.Targets, 1)
	assert.Equal(t, "example-http", cfg.Probes.Targets[0].Name)
	assert.Equal(t, 15*time.Second, cfg.Probes.Targets[0].Interval)
	assert.Equal(t, []int{200}, cfg.Probes.Targets[0].HTTP.ExpectedStatus)

	require.Len(t, cfg.Alerting.Rules, 1)
	assert.Equal(t, "Target Down", cfg.Alerting.Rules[0].Name)
	assert.Equal(t, "2m", cfg.Alerting.Rules[0].For)

	assert.Equal(t, "debug", cfg.Logging.Level)
}

// Test 2: Defaults application
func TestLoad_Defaults(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "minimal.yaml")
	minimal := `
storage:
  tsdb:
    path: "/tmp/test"
  metadata:
    path: "/tmp/meta.db"
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(minimal), 0644))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, ":8443", cfg.Server.Listen)
	assert.Equal(t, "https", cfg.Server.Mode)
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, 30*time.Second, cfg.Probes.Defaults.Interval)
	assert.Equal(t, 10*time.Second, cfg.Probes.Defaults.Timeout)
	assert.Equal(t, 2, cfg.Probes.Defaults.Retries)
}

// Test 3: Environment variable overrides
func TestLoad_EnvironmentOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "monstack.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

	t.Setenv("MONSTACK_SERVER_LISTEN", ":9090")
	t.Setenv("MONSTACK_LOGGING_LEVEL", "warn")
	t.Setenv("MONSTACK_AUTH_ADMIN_PASSWORD", "overridden-pass")

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Server.Listen)
	assert.Equal(t, "warn", cfg.Logging.Level)
	assert.Equal(t, "overridden-pass", cfg.Auth.AdminPassword)
}

// Test 4: File not found error
func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/path/to/nonexistent/monstack.yaml")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConfigNotFound)
}

// Test 5: Validation errors
func TestValidate_Failures(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(c *Config)
		errSubstr string
	}{
		{
			name: "missing server.listen",
			mutate: func(c *Config) {
				c.Server.Listen = ""
			},
			errSubstr: "server.listen is required",
		},
		{
			name: "invalid server.mode",
			mutate: func(c *Config) {
				c.Server.Mode = "invalid_mode"
			},
			errSubstr: "invalid server.mode",
		},
		{
			name: "manual TLS missing certs",
			mutate: func(c *Config) {
				c.Server.TLS.Mode = "manual"
				c.Server.TLS.Manual.CertFile = ""
			},
			errSubstr: "cert_file and key_file are required",
		},
		{
			name: "acme TLS missing email",
			mutate: func(c *Config) {
				c.Server.TLS.Mode = "acme"
				c.Server.TLS.ACME.Email = ""
				c.Server.TLS.ACME.Domains = []string{"example.com"}
			},
			errSubstr: "server.tls.acme.email is required",
		},
		{
			name: "acme TLS missing domains",
			mutate: func(c *Config) {
				c.Server.TLS.Mode = "acme"
				c.Server.TLS.ACME.Email = "admin@example.com"
				c.Server.TLS.ACME.Domains = nil
			},
			errSubstr: "server.tls.acme.domains must have at least one domain",
		},
		{
			name: "missing storage paths",
			mutate: func(c *Config) {
				c.Storage.TSDB.Path = ""
				c.Storage.Metadata.Path = ""
			},
			errSubstr: "storage.tsdb.path is required",
		},
		{
			name: "duplicate probe target name",
			mutate: func(c *Config) {
				c.Probes.Targets = []ProbeTargetConfig{
					{Name: "same", Type: "http", Target: "http://a"},
					{Name: "same", Type: "http", Target: "http://b"},
				}
			},
			errSubstr: "duplicate target name",
		},
		{
			name: "unsupported probe target type",
			mutate: func(c *Config) {
				c.Probes.Targets = []ProbeTargetConfig{
					{Name: "target-bad", Type: "unknown", Target: "http://a"},
				}
			},
			errSubstr: "is unsupported",
		},
		{
			name: "empty probe target address",
			mutate: func(c *Config) {
				c.Probes.Targets = []ProbeTargetConfig{
					{Name: "target-empty", Type: "http", Target: ""},
				}
			},
			errSubstr: "target is required",
		},
		{
			name: "invalid logging level",
			mutate: func(c *Config) {
				c.Logging.Level = "verbose"
			},
			errSubstr: "invalid logging.level",
		},
		{
			name: "invalid logging format",
			mutate: func(c *Config) {
				c.Logging.Format = "xml"
			},
			errSubstr: "invalid logging.format",
		},
		{
			name: "invalid alerting evaluation interval",
			mutate: func(c *Config) {
				c.Alerting.EvaluationInterval = 0
			},
			errSubstr: "alerting.evaluation_interval must be greater than zero",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgPath := filepath.Join(tmpDir, "monstack.yaml")
			require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

			cfg, err := Load(cfgPath)
			require.NoError(t, err)

			tt.mutate(cfg)
			err = Validate(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errSubstr)
		})
	}
}

// Test 6: Redaction masking and immutability
func TestRedact_SafeExport(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "monstack.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

	orig, err := Load(cfgPath)
	require.NoError(t, err)

	redacted := orig.Redacted()
	require.NotNil(t, redacted)

	// Verify original is intact
	assert.Equal(t, "supersecretpassword", orig.Auth.AdminPassword)
	assert.Equal(t, "$2a$10$hashedsecretpassword", orig.Auth.AdminPasswordHash)
	assert.Equal(t, "secret-ci-key-12345", orig.Auth.APIKeys[0].Key)
	assert.Equal(t, "myjwtsecretkey123", orig.Auth.JWT.Secret)
	assert.Equal(t, "123456:secret-bot-token", orig.Alerting.Channels[0].Config["bot_token"])
	assert.Equal(t, "Bearer secret-token-xyz", orig.Probes.Targets[0].HTTP.Headers["authorization"])

	// Verify redacted copy is masked
	assert.Equal(t, "***", redacted.Auth.AdminPassword)
	assert.Equal(t, "***", redacted.Auth.AdminPasswordHash)
	assert.Equal(t, "***", redacted.Auth.APIKeys[0].Key)
	assert.Equal(t, "***", redacted.Auth.JWT.Secret)
	assert.Equal(t, "***", redacted.Alerting.Channels[0].Config["bot_token"])
	assert.Equal(t, "-100123", redacted.Alerting.Channels[0].Config["chat_id"]) // non-sensitive preserved
	assert.Equal(t, "***", redacted.Probes.Targets[0].HTTP.Headers["authorization"])
	assert.Equal(t, "application/json", redacted.Probes.Targets[0].HTTP.Headers["content-type"])

	// Test nested map and slice redaction
	nestedCfg := &Config{
		Alerting: AlertingConfig{
			Channels: []AlertChannelConfig{
				{
					Name: "complex-channel",
					Type: "custom",
					Config: map[string]any{
						"api_secret": "topsecret",
						"nested_map": map[string]any{
							"token": "nested-token",
							"safe":  "public-info",
						},
						"slice_of_maps": []any{
							map[string]any{
								"password": "slice-password",
								"user":     "alice",
							},
							"plain-string",
						},
					},
				},
			},
		},
	}
	redactedNested := nestedCfg.Redacted()
	chConfig := redactedNested.Alerting.Channels[0].Config
	assert.Equal(t, "***", chConfig["api_secret"])
	nestedMap := chConfig["nested_map"].(map[string]any)
	assert.Equal(t, "***", nestedMap["token"])
	assert.Equal(t, "public-info", nestedMap["safe"])

	sliceOfMaps := chConfig["slice_of_maps"].([]any)
	mapInSlice := sliceOfMaps[0].(map[string]any)
	assert.Equal(t, "***", mapInSlice["password"])
	assert.Equal(t, "alice", mapInSlice["user"])
	assert.Equal(t, "plain-string", sliceOfMaps[1])

	// Nil receiver check
	var nilCfg *Config
	assert.Nil(t, nilCfg.Redacted())
	assert.Nil(t, nilCfg.Clone())
}

// Test 7: Watcher hot-reload and debounce
func TestWatcher_HotReloadAndDebounce(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "monstack.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

	initialCfg, err := Load(cfgPath)
	require.NoError(t, err)

	var reloadCount int32
	watcher, err := NewWatcher(cfgPath, initialCfg, slog.Default())
	require.NoError(t, err)
	defer watcher.Close()

	watcher.OnReload(func(oldCfg, newCfg *Config) error {
		atomic.AddInt32(&reloadCount, 1)
		return nil
	})

	// Perform 3 rapid file writes
	updatedContent := strings.Replace(sampleValidConfig, "level: debug", "level: warn", 1)
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(cfgPath, []byte(updatedContent), 0644))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce window (150ms + margin)
	time.Sleep(350 * time.Millisecond)

	assert.Equal(t, int32(1), atomic.LoadInt32(&reloadCount))
	assert.Equal(t, "warn", watcher.Config().Logging.Level)

	// Manual Reload test
	manualCfg, err := watcher.Reload()
	require.NoError(t, err)
	assert.Equal(t, "warn", manualCfg.Logging.Level)
}

// Test 8: Watcher invalid file preserves running config
func TestWatcher_InvalidFilePreservesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "monstack.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

	initialCfg, err := Load(cfgPath)
	require.NoError(t, err)

	watcher, err := NewWatcher(cfgPath, initialCfg, slog.Default())
	require.NoError(t, err)
	defer watcher.Close()

	// Write invalid YAML syntax
	require.NoError(t, os.WriteFile(cfgPath, []byte("bad: [broken yaml: :"), 0644))
	time.Sleep(300 * time.Millisecond)

	// Existing running config must be preserved
	assert.Equal(t, "debug", watcher.Config().Logging.Level)
	assert.Equal(t, ":8443", watcher.Config().Server.Listen)
}

// Test 9: Duration & Bytes parser helpers
func TestParseDuration_And_Bytes(t *testing.T) {
	// ParseDuration tests
	d, err := ParseDuration("30d")
	require.NoError(t, err)
	assert.Equal(t, 30*24*time.Hour, d)

	d, err = ParseDuration("1.5d")
	require.NoError(t, err)
	assert.Equal(t, 36*time.Hour, d)

	d, err = ParseDuration("48h")
	require.NoError(t, err)
	assert.Equal(t, 48*time.Hour, d)

	d, err = ParseDuration("")
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), d)

	_, err = ParseDuration("invalid-d")
	assert.Error(t, err)

	// ParseBytes tests
	b, err := ParseBytes("50GB")
	require.NoError(t, err)
	assert.Equal(t, int64(50*1024*1024*1024), b)

	b, err = ParseBytes("256MB")
	require.NoError(t, err)
	assert.Equal(t, int64(256*1024*1024), b)

	b, err = ParseBytes("16KB")
	require.NoError(t, err)
	assert.Equal(t, int64(16*1024), b)

	b, err = ParseBytes("2TB")
	require.NoError(t, err)
	assert.Equal(t, int64(2*1024*1024*1024*1024), b)

	b, err = ParseBytes("100B")
	require.NoError(t, err)
	assert.Equal(t, int64(100), b)

	b, err = ParseBytes("")
	require.NoError(t, err)
	assert.Equal(t, int64(0), b)

	_, err = ParseBytes("not-a-numberGB")
	assert.Error(t, err)
}

// Test 10: Load actual configs/examples/monstack.yaml
func TestLoad_ExampleMonStackYAML(t *testing.T) {
	cfg, err := Load("../../configs/examples/monstack.yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, ":8443", cfg.Server.Listen)
	assert.Equal(t, "https", cfg.Server.Mode)
	assert.Equal(t, "self-signed", cfg.Server.TLS.Mode)
	assert.Equal(t, "admin", cfg.Auth.AdminUser)
	assert.Equal(t, "changeme", cfg.Auth.AdminPassword)
	assert.Equal(t, "/var/lib/monstack/data", cfg.Storage.TSDB.Path)
	assert.Equal(t, "/var/lib/monstack/meta.db", cfg.Storage.Metadata.Path)
	assert.Equal(t, ":9443", cfg.GRPC.Listen)
	require.Len(t, cfg.Probes.Targets, 2)
	assert.Equal(t, "example-http", cfg.Probes.Targets[0].Name)
	assert.Equal(t, "gateway-ping", cfg.Probes.Targets[1].Name)
	require.Len(t, cfg.Alerting.Rules, 3)
	assert.Equal(t, "Target Down", cfg.Alerting.Rules[0].Name)
	assert.Equal(t, "High Latency", cfg.Alerting.Rules[1].Name)
	assert.Equal(t, "SSL Expiring", cfg.Alerting.Rules[2].Name)
}

// Test 11: applyTargetDefaults fallback with zero defaults
func TestApplyTargetDefaults_ZeroDefaults(t *testing.T) {
	cfg := &Config{
		Probes: ProbesConfig{
			Targets: []ProbeTargetConfig{
				{Name: "t1", Type: "http", Target: "http://example.com"},
			},
		},
	}
	applyTargetDefaults(cfg)
	assert.Equal(t, 30*time.Second, cfg.Probes.Targets[0].Interval)
	assert.Equal(t, 10*time.Second, cfg.Probes.Targets[0].Timeout)
	assert.Equal(t, 2, cfg.Probes.Targets[0].Retries)
	require.NotNil(t, cfg.Probes.Targets[0].Enabled)
	assert.True(t, *cfg.Probes.Targets[0].Enabled)
}

// Test 12: Watcher callback error handling
func TestWatcher_CallbackError(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "monstack.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sampleValidConfig), 0644))

	initialCfg, err := Load(cfgPath)
	require.NoError(t, err)

	watcher, err := NewWatcher(cfgPath, initialCfg, nil)
	require.NoError(t, err)
	defer watcher.Close()

	watcher.OnReload(func(oldCfg, newCfg *Config) error {
		return assert.AnError
	})

	_, err = watcher.Reload()
	assert.NoError(t, err) // Reload itself succeeds even if a callback returns error
}
