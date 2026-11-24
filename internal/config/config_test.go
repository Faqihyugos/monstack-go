package config

import (
	"encoding/json"
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

// Test 13: Redaction of hyphenated headers (X-API-Key, API-Key, Cookie, Private-Key) and immutability
func TestRedacted_HyphenatedHeadersAndImmutability(t *testing.T) {
	orig := &Config{
		Auth: AuthConfig{
			AdminPassword:     "my-super-secret-password",
			AdminPasswordHash: "$2a$10$hashedsecretpassword",
			APIKeys: []APIKeyConfig{
				{Name: "ci", Key: "ci-secret-token-12345"},
			},
			JWT: JWTConfig{Secret: "jwt-top-secret-signing-key"},
		},
		Alerting: AlertingConfig{
			Channels: []AlertChannelConfig{
				{
					Name: "webhook-sec",
					Type: "webhook",
					Config: map[string]any{
						"webhook_url": "https://hooks.slack.com/services/SECRET/URL/XYZ",
						"api-key":     "ch-secret-key-999",
						"cookie":      "session=ch-cookie-value",
						"public_name": "alerts-prod",
					},
				},
			},
		},
		Probes: ProbesConfig{
			Defaults: ProbeDefaultsConfig{
				Interval: 30 * time.Second,
				Timeout:  10 * time.Second,
				Retries:  2,
			},
			Targets: []ProbeTargetConfig{
				{
					Name:     "secure-api",
					Type:     "http",
					Target:   "https://api.example.com",
					Interval: 15 * time.Second,
					Timeout:  5 * time.Second,
					Retries:  2,
					HTTP: &HTTPProbeConfig{
						Method: "GET",
						Headers: map[string]string{
							"X-API-Key":      "my-secret-api-key",
							"API-Key":        "top-secret-api-key",
							"Cookie":         "session=abcdef1234567890",
							"Private-Key":    "-----BEGIN RSA PRIVATE KEY-----",
							"X-Service-Key":  "svc-secret-8888",
							"Authorization":  "Bearer token-jwt-12345",
							"X-Webhook-URL":  "https://discord.com/api/webhooks/secret",
							"Content-Type":   "application/json",
							"User-Agent":     "MonStack-Prober/1.0",
							"Accept":         "application/json",
							"X-Custom-Trace": "trace-uuid-112233",
						},
					},
				},
			},
		},
	}

	redacted := orig.Redacted()
	require.NotNil(t, redacted)

	// 1. Verify sensitive headers are masked with "***"
	redactedHeaders := redacted.Probes.Targets[0].HTTP.Headers
	assert.Equal(t, "***", redactedHeaders["X-API-Key"], "X-API-Key must be masked")
	assert.Equal(t, "***", redactedHeaders["API-Key"], "API-Key must be masked")
	assert.Equal(t, "***", redactedHeaders["Cookie"], "Cookie must be masked")
	assert.Equal(t, "***", redactedHeaders["Private-Key"], "Private-Key must be masked")
	assert.Equal(t, "***", redactedHeaders["X-Service-Key"], "X-Service-Key must be masked")
	assert.Equal(t, "***", redactedHeaders["Authorization"], "Authorization must be masked")
	assert.Equal(t, "***", redactedHeaders["X-Webhook-URL"], "X-Webhook-URL must be masked")

	// 2. Verify non-sensitive headers are preserved intact
	assert.Equal(t, "application/json", redactedHeaders["Content-Type"])
	assert.Equal(t, "MonStack-Prober/1.0", redactedHeaders["User-Agent"])
	assert.Equal(t, "application/json", redactedHeaders["Accept"])
	assert.Equal(t, "trace-uuid-112233", redactedHeaders["X-Custom-Trace"])

	// 3. Verify channel secrets are masked
	chCfg := redacted.Alerting.Channels[0].Config
	assert.Equal(t, "***", chCfg["webhook_url"])
	assert.Equal(t, "***", chCfg["api-key"])
	assert.Equal(t, "***", chCfg["cookie"])
	assert.Equal(t, "alerts-prod", chCfg["public_name"])

	// 4. Verify JSON marshaled output contains ZERO sensitive strings
	redactedJSON, err := json.Marshal(redacted)
	require.NoError(t, err)
	redactedStr := string(redactedJSON)

	sensitiveSecrets := []string{
		"my-secret-api-key",
		"top-secret-api-key",
		"session=abcdef1234567890",
		"-----BEGIN RSA PRIVATE KEY-----",
		"svc-secret-8888",
		"token-jwt-12345",
		"https://discord.com/api/webhooks/secret",
		"my-super-secret-password",
		"ci-secret-token-12345",
		"jwt-top-secret-signing-key",
		"https://hooks.slack.com/services/SECRET/URL/XYZ",
		"ch-secret-key-999",
		"ch-cookie-value",
	}
	for _, sec := range sensitiveSecrets {
		assert.False(t, strings.Contains(redactedStr, sec), "Redacted JSON must not leak secret: %s", sec)
	}

	// 5. Verify IMMUTABILITY of the original config
	origHeaders := orig.Probes.Targets[0].HTTP.Headers
	assert.Equal(t, "my-secret-api-key", origHeaders["X-API-Key"], "Original config must not be mutated")
	assert.Equal(t, "top-secret-api-key", origHeaders["API-Key"], "Original config must not be mutated")
	assert.Equal(t, "session=abcdef1234567890", origHeaders["Cookie"], "Original config must not be mutated")
	assert.Equal(t, "-----BEGIN RSA PRIVATE KEY-----", origHeaders["Private-Key"], "Original config must not be mutated")
	assert.Equal(t, "svc-secret-8888", origHeaders["X-Service-Key"], "Original config must not be mutated")
	assert.Equal(t, "Bearer token-jwt-12345", origHeaders["Authorization"], "Original config must not be mutated")
	assert.Equal(t, "https://discord.com/api/webhooks/secret", origHeaders["X-Webhook-URL"], "Original config must not be mutated")
	assert.Equal(t, "my-super-secret-password", orig.Auth.AdminPassword)
	assert.Equal(t, "ci-secret-token-12345", orig.Auth.APIKeys[0].Key)
	assert.Equal(t, "jwt-top-secret-signing-key", orig.Auth.JWT.Secret)
	assert.Equal(t, "https://hooks.slack.com/services/SECRET/URL/XYZ", orig.Alerting.Channels[0].Config["webhook_url"])
}

// Test 14: Non-positive probe intervals, timeouts, and retries validation
func TestValidate_NegativeAndZeroIntervals(t *testing.T) {
	baseCfg := func() *Config {
		return &Config{
			Server: ServerConfig{
				Listen: ":8443",
				Mode:   "http",
			},
			Storage: StorageConfig{
				TSDB: TSDBConfig{
					Path:      "/tmp/data",
					Retention: "30d",
					MaxDisk:   "50GB",
				},
				Metadata: MetadataConfig{Path: "/tmp/meta.db"},
			},
			Logging: LoggingConfig{Level: "info", Format: "json"},
			Alerting: AlertingConfig{
				EvaluationInterval: 10 * time.Second,
			},
			Probes: ProbesConfig{
				Defaults: ProbeDefaultsConfig{
					Interval: 30 * time.Second,
					Timeout:  10 * time.Second,
					Retries:  2,
				},
				Targets: []ProbeTargetConfig{
					{
						Name:     "t1",
						Type:     "http",
						Target:   "http://example.com",
						Interval: 15 * time.Second,
						Timeout:  5 * time.Second,
						Retries:  2,
					},
				},
			},
		}
	}

	t.Run("defaults.interval <= 0", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Defaults.Interval = 0
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.interval must be greater than zero")

		cfg.Probes.Defaults.Interval = -10 * time.Second
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.interval must be greater than zero")
	})

	t.Run("defaults.timeout <= 0", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Defaults.Timeout = 0
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.timeout must be greater than zero")

		cfg.Probes.Defaults.Timeout = -5 * time.Second
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.timeout must be greater than zero")
	})

	t.Run("defaults.timeout > defaults.interval", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Defaults.Interval = 10 * time.Second
		cfg.Probes.Defaults.Timeout = 15 * time.Second
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.timeout cannot be greater than interval")
	})

	t.Run("defaults.retries < 0", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Defaults.Retries = -1
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.retries must be non-negative")
	})

	t.Run("target.interval <= 0", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Targets[0].Interval = 0
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].interval must be greater than zero")

		cfg.Probes.Targets[0].Interval = -10 * time.Second
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].interval must be greater than zero")
	})

	t.Run("target.timeout <= 0", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Targets[0].Timeout = 0
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].timeout must be greater than zero")

		cfg.Probes.Targets[0].Timeout = -5 * time.Second
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].timeout must be greater than zero")
	})

	t.Run("target.timeout > target.interval", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Targets[0].Interval = 5 * time.Second
		cfg.Probes.Targets[0].Timeout = 10 * time.Second
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].timeout cannot be greater than interval")
	})

	t.Run("target.retries < 0", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Probes.Targets[0].Retries = -1
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].retries must be non-negative")
	})

	t.Run("Load with negative target interval YAML fails", func(t *testing.T) {
		yamlContent := `
server:
  listen: ":8443"
  mode: http
storage:
  tsdb:
    path: "/tmp/data"
    retention: 30d
    max_disk: 50GB
  metadata:
    path: "/tmp/meta.db"
probes:
  defaults:
    interval: 30s
    timeout: 10s
    retries: 2
  targets:
    - name: neg-target
      type: http
      target: http://example.com
      interval: -10s
`
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "monstack.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yamlContent), 0644))

		_, err := Load(cfgPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.targets[0].interval must be greater than zero")
	})

	t.Run("Load with negative default interval YAML fails", func(t *testing.T) {
		yamlContent := `
server:
  listen: ":8443"
  mode: http
storage:
  tsdb:
    path: "/tmp/data"
    retention: 30d
    max_disk: 50GB
  metadata:
    path: "/tmp/meta.db"
probes:
  defaults:
    interval: -10s
    timeout: 5s
    retries: 2
  targets:
    - name: test-target
      type: http
      target: http://example.com
`
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "monstack.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yamlContent), 0644))

		_, err := Load(cfgPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probes.defaults.interval must be greater than zero")
	})
}

// Test 15: TSDB retention and max_disk format validation
func TestValidate_TSDBRetentionAndMaxDisk(t *testing.T) {
	baseCfg := func() *Config {
		return &Config{
			Server: ServerConfig{
				Listen: ":8443",
				Mode:   "http",
			},
			Storage: StorageConfig{
				TSDB: TSDBConfig{
					Path:      "/tmp/data",
					Retention: "30d",
					MaxDisk:   "50GB",
				},
				Metadata: MetadataConfig{Path: "/tmp/meta.db"},
			},
			Logging: LoggingConfig{Level: "info", Format: "json"},
			Alerting: AlertingConfig{
				EvaluationInterval: 10 * time.Second,
			},
			Probes: ProbesConfig{
				Defaults: ProbeDefaultsConfig{
					Interval: 30 * time.Second,
					Timeout:  10 * time.Second,
					Retries:  2,
				},
				Targets: []ProbeTargetConfig{
					{
						Name:     "t1",
						Type:     "http",
						Target:   "http://example.com",
						Interval: 15 * time.Second,
						Timeout:  5 * time.Second,
						Retries:  2,
					},
				},
			},
		}
	}

	t.Run("invalid retention syntax", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Storage.TSDB.Retention = "invalid_retention"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid storage.tsdb.retention")
	})

	t.Run("zero retention duration", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Storage.TSDB.Retention = "0d"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "storage.tsdb.retention must be greater than zero")
	})

	t.Run("negative retention duration", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Storage.TSDB.Retention = "-10d"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "storage.tsdb.retention must be greater than zero")
	})

	t.Run("invalid max_disk syntax", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Storage.TSDB.MaxDisk = "INVALID_MAX_DISK"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid storage.tsdb.max_disk")

		cfg.Storage.TSDB.MaxDisk = "999XB"
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid storage.tsdb.max_disk")
	})

	t.Run("zero max_disk", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Storage.TSDB.MaxDisk = "0GB"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "storage.tsdb.max_disk must be greater than zero")
	})

	t.Run("negative max_disk", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Storage.TSDB.MaxDisk = "-50MB"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "storage.tsdb.max_disk must be greater than zero")
	})

	t.Run("Load with bogus retention YAML fails", func(t *testing.T) {
		yamlContent := `
server:
  listen: ":8443"
  mode: http
storage:
  tsdb:
    path: "/tmp/data"
    retention: "not_a_duration"
    max_disk: 50GB
  metadata:
    path: "/tmp/meta.db"
`
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "monstack.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yamlContent), 0644))

		_, err := Load(cfgPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid storage.tsdb.retention")
	})

	t.Run("Load with bogus max_disk YAML fails", func(t *testing.T) {
		yamlContent := `
server:
  listen: ":8443"
  mode: http
storage:
  tsdb:
    path: "/tmp/data"
    retention: 30d
    max_disk: "INVALID_BYTES"
  metadata:
    path: "/tmp/meta.db"
`
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "monstack.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yamlContent), 0644))

		_, err := Load(cfgPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid storage.tsdb.max_disk")
	})
}

// Test 16: Case-insensitive duration parsing
func TestParseDuration_CaseInsensitive(t *testing.T) {
	// Lowercase 'd'
	d, err := ParseDuration("30d")
	require.NoError(t, err)
	assert.Equal(t, 30*24*time.Hour, d)

	// Uppercase 'D'
	d, err = ParseDuration("30D")
	require.NoError(t, err)
	assert.Equal(t, 30*24*time.Hour, d)

	// Fractional uppercase 'D'
	d, err = ParseDuration("1.5D")
	require.NoError(t, err)
	assert.Equal(t, 36*time.Hour, d)

	// Uppercase hours, minutes, seconds
	d, err = ParseDuration("24H")
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, d)

	d, err = ParseDuration("10M")
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, d)

	d, err = ParseDuration("45S")
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, d)

	// Mixed case and spaces
	d, err = ParseDuration("  7D  ")
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, d)
}
