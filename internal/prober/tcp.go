package prober

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// TCPProber executes TCP connect, banner matching, and send/expect exchanges.
type TCPProber struct{}

// NewTCPProber creates a new TCPProber instance.
func NewTCPProber() *TCPProber {
	return &TCPProber{}
}

// resolvedTCPConfig holds the merged TCP configuration.
type resolvedTCPConfig struct {
	SendPayload string
	ExpectMatch string
	TLS         bool
}

// parseTCPConfig extracts and merges TCP configuration from target, supporting
// key aliases ("send" / "send_payload" and "expect" / "expect_match").
func parseTCPConfig(target *models.Target) resolvedTCPConfig {
	cfg := resolvedTCPConfig{}

	if target.Config == nil || len(target.Config) == 0 {
		return cfg
	}

	// 1. Try target.GetTCPConfig()
	if c, err := target.GetTCPConfig(); err == nil && c != nil {
		cfg.SendPayload = c.SendPayload
		cfg.ExpectMatch = c.ExpectMatch
		cfg.TLS = c.TLS
	}

	// 2. Check top-level aliases
	if s, ok := target.Config["send"].(string); ok && s != "" {
		cfg.SendPayload = s
	}
	if s, ok := target.Config["send_payload"].(string); ok && s != "" {
		cfg.SendPayload = s
	}
	if e, ok := target.Config["expect"].(string); ok && e != "" {
		cfg.ExpectMatch = e
	}
	if e, ok := target.Config["expect_match"].(string); ok && e != "" {
		cfg.ExpectMatch = e
	}
	if t, ok := target.Config["tls"].(bool); ok {
		cfg.TLS = t
	}

	// 3. Check nested "tcp" map
	if tcpRaw, ok := target.Config["tcp"]; ok {
		if tcpMap, ok := tcpRaw.(map[string]any); ok {
			if s, ok := tcpMap["send"].(string); ok && s != "" {
				cfg.SendPayload = s
			}
			if s, ok := tcpMap["send_payload"].(string); ok && s != "" {
				cfg.SendPayload = s
			}
			if e, ok := tcpMap["expect"].(string); ok && e != "" {
				cfg.ExpectMatch = e
			}
			if e, ok := tcpMap["expect_match"].(string); ok && e != "" {
				cfg.ExpectMatch = e
			}
			if t, ok := tcpMap["tls"].(bool); ok {
				cfg.TLS = t
			}
		}
	}

	return cfg
}

// Probe executes a TCP probe against target.Target (expected as host:port),
// measuring connect latency, verifying banners, and executing send/expect exchanges.
func (p *TCPProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	cfg := parseTCPConfig(target)

	now := time.Now().UTC()
	result := &models.ProbeResult{
		TargetID:   target.ID,
		TargetName: target.Name,
		TargetType: target.Type,
		Timestamp:  now,
		Metadata:   make(map[string]any),
	}

	timeout := 5 * time.Second
	if target.TimeoutMs > 0 {
		timeout = time.Duration(target.TimeoutMs) * time.Millisecond
	}

	// Measure connect latency
	connectStart := time.Now()
	var conn net.Conn
	var dialErr error

	if cfg.TLS {
		dialer := &net.Dialer{Timeout: timeout}
		tlsDialer := &tls.Dialer{
			NetDialer: dialer,
			Config:    &tls.Config{InsecureSkipVerify: true},
		}
		conn, dialErr = tlsDialer.DialContext(ctx, "tcp", target.Target)
	} else {
		dialer := &net.Dialer{Timeout: timeout}
		conn, dialErr = dialer.DialContext(ctx, "tcp", target.Target)
	}
	connectDuration := time.Since(connectStart)
	result.ResponseTime = connectDuration

	// Record connect latency metric
	result.Metrics = append(result.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricTCPConnectSeconds,
		Value:     connectDuration.Seconds(),
		Timestamp: now,
	})
	result.Metadata["connect_time_seconds"] = connectDuration.Seconds()

	if dialErr != nil {
		result.Success = false
		result.Error = dialErr.Error()
		return result, nil
	}
	defer conn.Close()

	// Apply socket deadline for read/write operations
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// Case 1: Send payload specified
	if cfg.SendPayload != "" {
		_, writeErr := conn.Write([]byte(cfg.SendPayload))
		if writeErr != nil {
			result.Success = false
			result.Error = fmt.Sprintf("failed to write TCP payload: %v", writeErr)
			return result, nil
		}

		if cfg.ExpectMatch != "" {
			buf := make([]byte, 4096)
			n, readErr := conn.Read(buf)
			if readErr != nil && n == 0 {
				result.Success = false
				result.Error = fmt.Sprintf("failed to read TCP response: %v", readErr)
				result.Metrics = append(result.Metrics, models.MetricPoint{
					TargetID:  target.ID,
					Metric:    models.MetricTCPBannerMatch,
					Value:     0.0,
					Timestamp: now,
				})
				return result, nil
			}
			respStr := string(buf[:n])
			result.Metadata["received_payload"] = respStr

			matched := strings.Contains(respStr, cfg.ExpectMatch)
			if !matched {
				if re, err := regexp.Compile(cfg.ExpectMatch); err == nil && re.MatchString(respStr) {
					matched = true
				}
			}

			if matched {
				result.Success = true
				result.Metrics = append(result.Metrics, models.MetricPoint{
					TargetID:  target.ID,
					Metric:    models.MetricTCPBannerMatch,
					Value:     1.0,
					Timestamp: now,
				})
			} else {
				result.Success = false
				result.Error = fmt.Sprintf("TCP response %q did not match expected %q", respStr, cfg.ExpectMatch)
				result.Metrics = append(result.Metrics, models.MetricPoint{
					TargetID:  target.ID,
					Metric:    models.MetricTCPBannerMatch,
					Value:     0.0,
					Timestamp: now,
				})
			}
			return result, nil
		}

		result.Success = true
		return result, nil
	}

	// Case 2: No send payload, but expect specified -> Banner Greeting Match
	if cfg.ExpectMatch != "" {
		buf := make([]byte, 4096)
		n, readErr := conn.Read(buf)
		if readErr != nil && n == 0 {
			result.Success = false
			result.Error = fmt.Sprintf("failed to read TCP banner: %v", readErr)
			result.Metrics = append(result.Metrics, models.MetricPoint{
				TargetID:  target.ID,
				Metric:    models.MetricTCPBannerMatch,
				Value:     0.0,
				Timestamp: now,
			})
			return result, nil
		}
		bannerStr := string(buf[:n])
		result.Metadata["banner"] = bannerStr

		matched := strings.Contains(bannerStr, cfg.ExpectMatch)
		if !matched {
			if re, err := regexp.Compile(cfg.ExpectMatch); err == nil && re.MatchString(bannerStr) {
				matched = true
			}
		}

		if matched {
			result.Success = true
			result.Metrics = append(result.Metrics, models.MetricPoint{
				TargetID:  target.ID,
				Metric:    models.MetricTCPBannerMatch,
				Value:     1.0,
				Timestamp: now,
			})
		} else {
			result.Success = false
			result.Error = fmt.Sprintf("TCP banner %q did not match expected %q", bannerStr, cfg.ExpectMatch)
			result.Metrics = append(result.Metrics, models.MetricPoint{
				TargetID:  target.ID,
				Metric:    models.MetricTCPBannerMatch,
				Value:     0.0,
				Timestamp: now,
			})
		}
		return result, nil
	}

	// Case 3: Pure port reachability check
	result.Success = true
	return result, nil
}
