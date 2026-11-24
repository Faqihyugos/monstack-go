package prober

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// UDPProber executes UDP connectivity and request/response probes.
type UDPProber struct{}

// NewUDPProber creates a new UDPProber.
func NewUDPProber() *UDPProber {
	return &UDPProber{}
}

// Probe dials a UDP target (host:port), optionally sends a payload, and verifies any expected response.
func (p *UDPProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	now := time.Now().UTC()
	res := &models.ProbeResult{
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

	sendPayload := getConfigString(target.Config, "send_payload", "send")
	expectMatch := getConfigString(target.Config, "expect_match", "expect")

	start := time.Now()
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "udp", target.Target)
	if err != nil {
		res.ResponseTime = time.Since(start)
		res.Success = false
		res.Error = fmt.Sprintf("udp dial failed: %v", err)
		return res, nil
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))

	if sendPayload == "" {
		sendPayload = "\n"
	}
	if _, err := conn.Write([]byte(sendPayload)); err != nil {
		res.ResponseTime = time.Since(start)
		res.Success = false
		res.Error = fmt.Sprintf("udp write failed: %v", err)
		return res, nil
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	elapsed := time.Since(start)
	res.ResponseTime = elapsed
	res.Metrics = append(res.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricUDPResponseSeconds,
		Value:     elapsed.Seconds(),
		Timestamp: now,
	})

	if err != nil && n == 0 {
		res.Success = false
		res.Error = fmt.Sprintf("udp read failed: %v", err)
		return res, nil
	}

	respStr := string(buf[:n])
	res.Metadata["received_payload"] = respStr

	if expectMatch != "" {
		matched := strings.Contains(respStr, expectMatch)
		if !matched {
			if re, reErr := regexp.Compile(expectMatch); reErr == nil && re.MatchString(respStr) {
				matched = true
			}
		}
		if !matched {
			res.Success = false
			res.Error = fmt.Sprintf("udp response %q did not match expected %q", respStr, expectMatch)
			return res, nil
		}
	}

	res.Success = true
	return res, nil
}

func getConfigString(cfg map[string]any, keys ...string) string {
	if cfg == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := cfg[k]; ok && v != nil {
			s := fmt.Sprintf("%v", v)
			if s != "" {
				return s
			}
		}
	}
	return ""
}
