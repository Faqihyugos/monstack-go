package prober

import (
	"context"
	"fmt"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/yourusername/monstack-go/pkg/models"
)

// ICMPProber executes ICMP ping probes using unprivileged UDP mode.
type ICMPProber struct{}

// NewICMPProber creates a new ICMPProber instance.
func NewICMPProber() *ICMPProber {
	return &ICMPProber{}
}

// resolvedICMPConfig holds the merged ICMP configuration.
type resolvedICMPConfig struct {
	Count      int
	PacketSize int
	Protocol   string
}

// parseICMPConfig extracts and merges ICMP configuration from target.
func parseICMPConfig(target *models.Target) resolvedICMPConfig {
	cfg := resolvedICMPConfig{
		Count:      3,
		PacketSize: 64,
		Protocol:   "ip4",
	}

	if target.Config == nil || len(target.Config) == 0 {
		return cfg
	}

	// 1. Check target.GetICMPConfig()
	if c, err := target.GetICMPConfig(); err == nil && c != nil {
		if c.Count > 0 {
			cfg.Count = c.Count
		}
		if c.PacketSize > 0 {
			cfg.PacketSize = c.PacketSize
		}
		if c.Protocol != "" {
			cfg.Protocol = c.Protocol
		}
	}

	// 2. Check nested "icmp" map
	if icmpRaw, ok := target.Config["icmp"]; ok {
		if icmpMap, ok := icmpRaw.(map[string]any); ok {
			if cnt, ok := icmpMap["count"].(float64); ok && cnt > 0 {
				cfg.Count = int(cnt)
			} else if cnt, ok := icmpMap["count"].(int); ok && cnt > 0 {
				cfg.Count = cnt
			}
			if ps, ok := icmpMap["packet_size"].(float64); ok && ps > 0 {
				cfg.PacketSize = int(ps)
			} else if ps, ok := icmpMap["packet_size"].(int); ok && ps > 0 {
				cfg.PacketSize = ps
			}
			if proto, ok := icmpMap["protocol"].(string); ok && proto != "" {
				cfg.Protocol = proto
			}
		}
	}

	return cfg
}

// Probe executes an ICMP ping sequence using pro-bing in unprivileged UDP mode,
// measuring round trip times (avg, min, max, jitter) and packet loss percentage.
func (p *ICMPProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	cfg := parseICMPConfig(target)

	now := time.Now().UTC()
	result := &models.ProbeResult{
		TargetID:   target.ID,
		TargetName: target.Name,
		TargetType: target.Type,
		Timestamp:  now,
		Metadata:   make(map[string]any),
	}

	// Check if target has disallowed URL scheme
	if strings.HasPrefix(target.Target, "http://") || strings.HasPrefix(target.Target, "https://") {
		result.Success = false
		result.Error = fmt.Sprintf("ICMP target must not have a URL scheme: %s", target.Target)
		result.Metrics = append(result.Metrics, models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricICMPPacketLoss,
			Value:     100.0,
			Timestamp: now,
		})
		return result, nil
	}

	// Initialize pinger with target address
	pinger, err := probing.NewPinger(target.Target)
	if err != nil {
		// Host resolution failure or invalid target
		result.Success = false
		result.Error = fmt.Sprintf("failed to resolve ICMP target %s: %v", target.Target, err)
		result.Metrics = append(result.Metrics, models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricICMPPacketLoss,
			Value:     100.0,
			Timestamp: now,
		})
		return result, nil
	}

	// Force unprivileged UDP mode (works on macOS and unprivileged Linux)
	pinger.SetPrivileged(false)

	// Configure packet parameters
	pinger.Count = cfg.Count
	pinger.Size = cfg.PacketSize
	if cfg.Protocol == "ip6" {
		pinger.SetNetwork("ip6")
	} else {
		pinger.SetNetwork("ip4")
	}

	// Determine timeout
	timeout := 3 * time.Second
	if target.TimeoutMs > 0 {
		timeout = time.Duration(target.TimeoutMs) * time.Millisecond
	}
	pinger.Timeout = timeout

	// Calculate packet interval to ensure all packets are sent within timeout window
	interval := 50 * time.Millisecond
	if cfg.Count > 1 {
		calcInterval := timeout / time.Duration(cfg.Count+1)
		if calcInterval < interval {
			interval = calcInterval
		}
	}
	pinger.Interval = interval

	// Execute ICMP pings
	startTime := time.Now()
	runErr := pinger.RunWithContext(ctx)
	duration := time.Since(startTime)
	result.ResponseTime = duration

	stats := pinger.Statistics()
	var packetLoss float64 = 100.0
	var avgRttMs, minRttMs, maxRttMs, jitterMs float64

	if stats != nil {
		packetLoss = stats.PacketLoss
		avgRttMs = float64(stats.AvgRtt.Nanoseconds()) / 1e6
		minRttMs = float64(stats.MinRtt.Nanoseconds()) / 1e6
		maxRttMs = float64(stats.MaxRtt.Nanoseconds()) / 1e6
		jitterMs = float64(stats.StdDevRtt.Nanoseconds()) / 1e6

		result.Metadata["packets_sent"] = stats.PacketsSent
		result.Metadata["packets_recv"] = stats.PacketsRecv
		result.Metadata["packet_loss_percent"] = packetLoss
		result.Metadata["rtt_avg_ms"] = avgRttMs
		result.Metadata["rtt_min_ms"] = minRttMs
		result.Metadata["rtt_max_ms"] = maxRttMs
		result.Metadata["jitter_ms"] = jitterMs
	}

	// Record packet loss metric
	result.Metrics = append(result.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricICMPPacketLoss,
		Value:     packetLoss,
		Timestamp: now,
	})

	// Record RTT and jitter metrics
	result.Metrics = append(result.Metrics,
		models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricICMPRTTAverage,
			Value:     avgRttMs,
			Timestamp: now,
		},
		models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricICMPRTTMin,
			Value:     minRttMs,
			Timestamp: now,
		},
		models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricICMPRTTMax,
			Value:     maxRttMs,
			Timestamp: now,
		},
		models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricICMPJitter,
			Value:     jitterMs,
			Timestamp: now,
		},
	)

	// Evaluate success
	if stats != nil && stats.PacketsRecv > 0 && packetLoss < 100.0 {
		result.Success = true
	} else {
		result.Success = false
		if runErr != nil {
			result.Error = fmt.Sprintf("ICMP ping failed: %v", runErr)
		} else {
			sent := 0
			if stats != nil {
				sent = stats.PacketsSent
			}
			result.Error = fmt.Sprintf("ICMP ping 100%% packet loss (%d sent, 0 received)", sent)
		}
	}

	return result, nil
}
