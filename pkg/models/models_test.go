package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Target Tests ---

func TestTargetType_IsValid(t *testing.T) {
	validTypes := []TargetType{
		TargetTypeHTTP, TargetTypeICMP, TargetTypeTCP, TargetTypeUDP,
		TargetTypeDNS, TargetTypeSNMP, TargetTypeGRPC, TargetTypeTLS, TargetTypePortScan,
	}
	for _, tt := range validTypes {
		assert.True(t, tt.IsValid(), "expected valid for %s", tt)
	}

	invalidTypes := []TargetType{"websocket", "ftp", "", "INVALID"}
	for _, tt := range invalidTypes {
		assert.False(t, tt.IsValid(), "expected invalid for %s", tt)
	}
}

func TestTarget_Validate_Success(t *testing.T) {
	tests := []struct {
		name   string
		target *Target
	}{
		{
			name: "Valid HTTP target",
			target: &Target{
				Name:       "example-http",
				Type:       TargetTypeHTTP,
				Target:     "https://example.com/health",
				IntervalMs: 15000,
				TimeoutMs:  5000,
				Retries:    2,
			},
		},
		{
			name: "Valid TCP target",
			target: &Target{
				Name:       "postgres-tcp",
				Type:       TargetTypeTCP,
				Target:     "127.0.0.1:5432",
				IntervalMs: 10000,
				TimeoutMs:  2000,
				Retries:    1,
			},
		},
		{
			name: "Valid ICMP target",
			target: &Target{
				Name:       "gateway-ping",
				Type:       TargetTypeICMP,
				Target:     "192.168.1.1",
				IntervalMs: 5000,
				TimeoutMs:  2000,
				Retries:    0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.target.Validate())
		})
	}
}

func TestTarget_Validate_Failures(t *testing.T) {
	tests := []struct {
		name      string
		target    *Target
		errSubstr string
	}{
		{
			name: "Empty name",
			target: &Target{
				Name:       "",
				Type:       TargetTypeHTTP,
				Target:     "http://example.com",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "target name cannot be empty",
		},
		{
			name: "Name too long",
			target: &Target{
				Name:       string(make([]byte, 129)),
				Type:       TargetTypeHTTP,
				Target:     "http://example.com",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "target name exceeds 128 characters",
		},
		{
			name: "Invalid type",
			target: &Target{
				Name:       "test",
				Type:       "unknown-type",
				Target:     "http://example.com",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "invalid target type",
		},
		{
			name: "Empty target address",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeHTTP,
				Target:     "",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "target address cannot be empty",
		},
		{
			name: "Non-positive interval",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeHTTP,
				Target:     "http://example.com",
				IntervalMs: 0,
				TimeoutMs:  500,
			},
			errSubstr: "interval_ms must be greater than 0",
		},
		{
			name: "Non-positive timeout",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeHTTP,
				Target:     "http://example.com",
				IntervalMs: 1000,
				TimeoutMs:  -5,
			},
			errSubstr: "timeout_ms must be greater than 0",
		},
		{
			name: "Timeout exceeds interval",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeHTTP,
				Target:     "http://example.com",
				IntervalMs: 1000,
				TimeoutMs:  2000,
			},
			errSubstr: "timeout_ms cannot exceed interval_ms",
		},
		{
			name: "Negative retries",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeHTTP,
				Target:     "http://example.com",
				IntervalMs: 1000,
				TimeoutMs:  500,
				Retries:    -1,
			},
			errSubstr: "retries cannot be negative",
		},
		{
			name: "Invalid HTTP scheme",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeHTTP,
				Target:     "ftp://example.com",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "invalid HTTP target URL",
		},
		{
			name: "Invalid TCP address format",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeTCP,
				Target:     "example.com",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "invalid TCP target",
		},
		{
			name: "ICMP with URL scheme",
			target: &Target{
				Name:       "test",
				Type:       TargetTypeICMP,
				Target:     "http://8.8.8.8",
				IntervalMs: 1000,
				TimeoutMs:  500,
			},
			errSubstr: "ICMP target must not have a URL scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errSubstr)
		})
	}
}

func TestNewTarget(t *testing.T) {
	tg := NewTarget("web", TargetTypeHTTP, "https://example.com", 0, 0)
	assert.Equal(t, "web", tg.Name)
	assert.Equal(t, TargetTypeHTTP, tg.Type)
	assert.Equal(t, "https://example.com", tg.Target)
	assert.Equal(t, 30000, tg.IntervalMs)
	assert.Equal(t, 10000, tg.TimeoutMs)
	assert.Equal(t, 2, tg.Retries)
	assert.True(t, tg.Enabled)
	assert.False(t, tg.CreatedAt.IsZero())
	assert.False(t, tg.UpdatedAt.IsZero())
}

func TestTarget_ConfigExtractors(t *testing.T) {
	t.Run("HTTPConfig extraction", func(t *testing.T) {
		tg := &Target{
			Config: map[string]any{
				"method":           "POST",
				"expected_status":  []int{201, 204},
				"follow_redirects": false,
				"max_redirects":    5,
				"tls_skip_verify":  true,
				"headers": map[string]string{
					"Authorization": "Bearer token",
				},
			},
		}
		cfg, err := tg.GetHTTPConfig()
		require.NoError(t, err)
		assert.Equal(t, "POST", cfg.Method)
		assert.Equal(t, []int{201, 204}, cfg.ExpectedStatus)
		assert.False(t, cfg.FollowRedirects)
		assert.Equal(t, 5, cfg.MaxRedirects)
		assert.True(t, cfg.TLSSkipVerify)
		assert.Equal(t, "Bearer token", cfg.Headers["Authorization"])

		// Default fallback
		emptyTg := &Target{}
		emptyCfg, err := emptyTg.GetHTTPConfig()
		require.NoError(t, err)
		assert.Equal(t, "GET", emptyCfg.Method)
		assert.Equal(t, []int{200}, emptyCfg.ExpectedStatus)
	})

	t.Run("ICMPConfig extraction", func(t *testing.T) {
		tg := &Target{
			Config: map[string]any{
				"count":       5,
				"packet_size": 128,
				"protocol":    "ip6",
			},
		}
		cfg, err := tg.GetICMPConfig()
		require.NoError(t, err)
		assert.Equal(t, 5, cfg.Count)
		assert.Equal(t, 128, cfg.PacketSize)
		assert.Equal(t, "ip6", cfg.Protocol)

		emptyTg := &Target{}
		emptyCfg, err := emptyTg.GetICMPConfig()
		require.NoError(t, err)
		assert.Equal(t, 3, emptyCfg.Count)
		assert.Equal(t, 64, emptyCfg.PacketSize)
		assert.Equal(t, "ip4", emptyCfg.Protocol)
	})

	t.Run("TCPConfig extraction", func(t *testing.T) {
		tg := &Target{
			Config: map[string]any{
				"send_payload": "PING\r\n",
				"expect_match": "PONG",
				"tls":          true,
			},
		}
		cfg, err := tg.GetTCPConfig()
		require.NoError(t, err)
		assert.Equal(t, "PING\r\n", cfg.SendPayload)
		assert.Equal(t, "PONG", cfg.ExpectMatch)
		assert.True(t, cfg.TLS)
	})
}

func TestTarget_JSONRoundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tg := &Target{
		ID:         "target-123",
		Name:       "example",
		Type:       TargetTypeHTTP,
		Target:     "https://example.com",
		IntervalMs: 15000,
		TimeoutMs:  5000,
		Retries:    2,
		Config:     map[string]any{"method": "GET"},
		Labels:     map[string]string{"env": "prod"},
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	data, err := json.Marshal(tg)
	require.NoError(t, err)

	var decoded Target
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, tg.ID, decoded.ID)
	assert.Equal(t, tg.Name, decoded.Name)
	assert.Equal(t, tg.Type, decoded.Type)
	assert.Equal(t, tg.Target, decoded.Target)
	assert.Equal(t, tg.IntervalMs, decoded.IntervalMs)
	assert.Equal(t, tg.Labels["env"], decoded.Labels["env"])
	assert.True(t, tg.CreatedAt.Equal(decoded.CreatedAt))
}

// --- Metric Tests ---

func TestMetricPoint_Validate(t *testing.T) {
	now := time.Now().UTC()
	valid := &MetricPoint{
		TargetID:  "t1",
		Metric:    MetricProbeSuccess,
		Value:     1.0,
		Timestamp: now,
	}
	assert.NoError(t, valid.Validate())

	assert.Error(t, (&MetricPoint{Metric: "m", Timestamp: now}).Validate())
	assert.Error(t, (&MetricPoint{TargetID: "t", Timestamp: now}).Validate())
	assert.Error(t, (&MetricPoint{TargetID: "t", Metric: "m"}).Validate())
}

func TestProbeResult_Validate_And_ToMetricPoints(t *testing.T) {
	now := time.Now().UTC()
	result := &ProbeResult{
		TargetID:     "t-http-1",
		TargetName:   "Google",
		TargetType:   TargetTypeHTTP,
		Success:      true,
		ResponseTime: 120 * time.Millisecond,
		Timestamp:    now,
		Metrics: []MetricPoint{
			{Metric: MetricHTTPStatusCode, Value: 200},
			{Metric: MetricHTTPTLSHandshakeSeconds, Value: 0.045},
		},
	}

	require.NoError(t, result.Validate())

	// Test validation errors
	assert.Error(t, (&ProbeResult{TargetType: TargetTypeHTTP, Timestamp: now}).Validate())
	assert.Error(t, (&ProbeResult{TargetID: "t", TargetType: "invalid", Timestamp: now}).Validate())
	assert.Error(t, (&ProbeResult{TargetID: "t", TargetType: TargetTypeHTTP}).Validate())

	points := result.ToMetricPoints()
	require.Len(t, points, 4) // success, duration, status_code, tls_handshake

	assert.Equal(t, MetricProbeSuccess, points[0].Metric)
	assert.Equal(t, 1.0, points[0].Value)
	assert.Equal(t, "t-http-1", points[0].TargetID)

	assert.Equal(t, MetricProbeDurationSeconds, points[1].Metric)
	assert.InDelta(t, 0.12, points[1].Value, 0.0001)

	assert.Equal(t, MetricHTTPStatusCode, points[2].Metric)
	assert.Equal(t, 200.0, points[2].Value)
	assert.Equal(t, "t-http-1", points[2].TargetID)
	assert.Equal(t, now, points[2].Timestamp)

	assert.Equal(t, MetricHTTPTLSHandshakeSeconds, points[3].Metric)
	assert.Equal(t, 0.045, points[3].Value)

	// Failure case
	failResult := &ProbeResult{
		TargetID:     "t-http-2",
		TargetType:   TargetTypeHTTP,
		Success:      false,
		ResponseTime: 5 * time.Second,
		Error:        "connection refused",
		Timestamp:    now,
	}
	failPoints := failResult.ToMetricPoints()
	require.Len(t, failPoints, 2)
	assert.Equal(t, 0.0, failPoints[0].Value)
}

// --- Alert Tests ---

func TestAlertEnums(t *testing.T) {
	assert.True(t, SeverityInfo.IsValid())
	assert.True(t, SeverityWarning.IsValid())
	assert.True(t, SeverityCritical.IsValid())
	assert.False(t, AlertSeverity("fatal").IsValid())

	assert.True(t, AlertStatusPending.IsValid())
	assert.True(t, AlertStatusFiring.IsValid())
	assert.True(t, AlertStatusResolved.IsValid())
	assert.False(t, AlertStatus("unknown").IsValid())
}

func TestAlertRule_Validate_And_ForDuration(t *testing.T) {
	rule := &AlertRule{
		Name:        "High Latency",
		Expression:  "probe_response_time > 2",
		ForDuration: "2m",
		Severity:    SeverityWarning,
	}
	assert.NoError(t, rule.Validate())
	assert.Equal(t, 2*time.Minute, rule.ForDurationParsed())

	// Edge cases
	emptyRule := &AlertRule{}
	assert.Error(t, emptyRule.Validate())

	badDurationRule := &AlertRule{
		Name:        "Test",
		Expression:  "expr",
		ForDuration: "invalid-duration",
		Severity:    SeverityCritical,
	}
	assert.Error(t, badDurationRule.Validate())
	assert.Equal(t, time.Duration(0), badDurationRule.ForDurationParsed())

	zeroDurationRule := &AlertRule{
		Name:       "Test",
		Expression: "expr",
		Severity:   SeverityInfo,
	}
	assert.NoError(t, zeroDurationRule.Validate())
	assert.Equal(t, time.Duration(0), zeroDurationRule.ForDurationParsed())
}

func TestAlertEvent_Validate(t *testing.T) {
	now := time.Now().UTC()
	validEvent := &AlertEvent{
		RuleID:    "r1",
		Status:    AlertStatusFiring,
		Severity:  SeverityCritical,
		FiredAt:   now,
		Message:   "Target down",
	}
	assert.NoError(t, validEvent.Validate())

	assert.Error(t, (&AlertEvent{Status: AlertStatusFiring, Severity: SeverityCritical, FiredAt: now}).Validate())
	assert.Error(t, (&AlertEvent{RuleID: "r1", Status: "bogus", Severity: SeverityCritical, FiredAt: now}).Validate())
	assert.Error(t, (&AlertEvent{RuleID: "r1", Status: AlertStatusFiring, Severity: "bogus", FiredAt: now}).Validate())
	assert.Error(t, (&AlertEvent{RuleID: "r1", Status: AlertStatusFiring, Severity: SeverityCritical}).Validate())
}

func TestAlertSilence_Validate_And_Active(t *testing.T) {
	now := time.Now().UTC()
	silence := &AlertSilence{
		ID:        "s1",
		Matchers:  `{"target_name":"prod-db"}`,
		StartsAt:  now.Add(-10 * time.Minute),
		EndsAt:    now.Add(10 * time.Minute),
		Comment:   "Maintenance window",
		CreatedBy: "admin",
	}
	assert.NoError(t, silence.Validate())
	assert.True(t, silence.IsActive(now))
	assert.False(t, silence.IsActive(now.Add(-15*time.Minute)))
	assert.False(t, silence.IsActive(now.Add(15*time.Minute)))

	// Invalid cases
	assert.Error(t, (&AlertSilence{StartsAt: now, EndsAt: now.Add(-time.Hour), Matchers: "x"}).Validate())
	assert.Error(t, (&AlertSilence{StartsAt: now, EndsAt: now.Add(time.Hour), Matchers: ""}).Validate())
}

func TestAlertSilence_Matches(t *testing.T) {
	now := time.Now().UTC()

	// JSON matcher
	jsonSilence := &AlertSilence{
		Matchers: `{"target":"api-gateway", "env":"production"}`,
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
	}
	assert.True(t, jsonSilence.Matches("api-gateway", map[string]string{"env": "production", "region": "us-east"}))
	assert.False(t, jsonSilence.Matches("other-service", map[string]string{"env": "production"}))
	assert.False(t, jsonSilence.Matches("api-gateway", map[string]string{"env": "staging"}))

	// Key-value pairs matcher
	kvSilence := &AlertSilence{
		Matchers: `target_name=worker-node, tier=backend`,
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
	}
	assert.True(t, kvSilence.Matches("worker-node", map[string]string{"tier": "backend"}))
	assert.False(t, kvSilence.Matches("worker-node", map[string]string{"tier": "frontend"}))

	// Plain substring fallback
	plainSilence := &AlertSilence{
		Matchers: "prod-db",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
	}
	assert.True(t, plainSilence.Matches("prod-db-primary", nil))
	assert.False(t, plainSilence.Matches("staging-db", nil))
	assert.False(t, (&AlertSilence{}).Matches("test", nil))
}
