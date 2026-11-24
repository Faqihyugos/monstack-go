package prober

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// DNSProber executes DNS resolution probes using miekg/dns.
type DNSProber struct{}

// NewDNSProber creates a new DNSProber.
func NewDNSProber() *DNSProber {
	return &DNSProber{}
}

// Probe queries a DNS server for the target domain and validates record count and optional expected answer.
func (p *DNSProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
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

	server := getConfigString(target.Config, "server", "nameserver")
	if server == "" {
		server = "8.8.8.8:53"
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}

	qTypeStr := strings.ToUpper(strings.TrimSpace(getConfigString(target.Config, "record_type", "qtype")))
	if qTypeStr == "" {
		qTypeStr = "A"
	}
	qType, ok := dns.StringToType[qTypeStr]
	if !ok {
		res.Success = false
		res.Error = fmt.Sprintf("unsupported DNS record type: %q", qTypeStr)
		return res, nil
	}

	proto := strings.ToLower(strings.TrimSpace(getConfigString(target.Config, "protocol")))
	if proto != "tcp" {
		proto = "udp"
	}
	expectAnswer := getConfigString(target.Config, "expect_answer", "expect")

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(target.Target), qType)
	msg.RecursionDesired = true

	client := &dns.Client{
		Net:     proto,
		Timeout: timeout,
	}

	start := time.Now()
	resp, rtt, err := client.ExchangeContext(ctx, msg, server)
	elapsed := time.Since(start)
	if rtt > 0 {
		elapsed = rtt
	}
	res.ResponseTime = elapsed

	res.Metrics = append(res.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricDNSResolveSeconds,
		Value:     elapsed.Seconds(),
		Timestamp: now,
	})

	if err != nil {
		res.Success = false
		res.Error = fmt.Sprintf("dns exchange failed: %v", err)
		return res, nil
	}

	res.Metadata["rcode"] = dns.RcodeToString[resp.Rcode]
	res.Metadata["authoritative"] = resp.Authoritative
	res.Metadata["server"] = server
	res.Metadata["record_type"] = qTypeStr

	if resp.Rcode != dns.RcodeSuccess {
		res.Success = false
		res.Error = fmt.Sprintf("dns query returned non-success rcode: %s", dns.RcodeToString[resp.Rcode])
		return res, nil
	}

	answers := make([]string, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		answers = append(answers, rr.String())
	}
	res.Metadata["answers"] = answers
	res.Metrics = append(res.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricDNSRecordCount,
		Value:     float64(len(answers)),
		Timestamp: now,
	})

	if expectAnswer != "" {
		found := false
		for _, ans := range answers {
			if strings.Contains(ans, expectAnswer) {
				found = true
				break
			}
		}
		if !found {
			res.Success = false
			res.Error = fmt.Sprintf("dns answers %v did not contain expected %q", answers, expectAnswer)
			return res, nil
		}
	}

	res.Success = true
	return res, nil
}
