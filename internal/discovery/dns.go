package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// DNSSRVDiscoverer discovers targets by querying DNS SRV records.
type DNSSRVDiscoverer struct {
	domain    string
	server    string
	probeType models.TargetType
	interval  time.Duration
	logger    *slog.Logger
}

// NewDNSSRVDiscoverer creates a DNS SRV discoverer from configuration.
func NewDNSSRVDiscoverer(cfg map[string]any, logger *slog.Logger) (*DNSSRVDiscoverer, error) {
	domain := getStr(cfg, "domain", "name")
	if domain == "" {
		return nil, errors.New("dns_srv discovery requires 'domain'")
	}
	probeType := models.TargetType(strings.ToLower(getStr(cfg, "probe_type")))
	if !probeType.IsValid() {
		probeType = models.TargetTypeTCP
	}
	return &DNSSRVDiscoverer{
		domain:    domain,
		server:    getStr(cfg, "server", "nameserver"),
		probeType: probeType,
		interval:  getDuration(cfg, 60*time.Second, "refresh_interval", "interval"),
		logger:    logger,
	}, nil
}

func (d *DNSSRVDiscoverer) Name() string            { return "dns_srv:" + d.domain }
func (d *DNSSRVDiscoverer) Interval() time.Duration { return d.interval }

// Discover queries SRV records and builds corresponding Target definitions.
func (d *DNSSRVDiscoverer) Discover(ctx context.Context) ([]*models.Target, error) {
	var srvs []*dns.SRV

	if d.server != "" {
		server := d.server
		if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(server, "53")
		}
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(d.domain), dns.TypeSRV)
		msg.RecursionDesired = true

		client := &dns.Client{Timeout: 5 * time.Second}
		resp, _, err := client.ExchangeContext(ctx, msg, server)
		if err != nil {
			return nil, fmt.Errorf("dns_srv query failed: %w", err)
		}
		for _, ans := range resp.Answer {
			if srv, ok := ans.(*dns.SRV); ok {
				srvs = append(srvs, srv)
			}
		}
	} else {
		_, addrs, err := net.DefaultResolver.LookupSRV(ctx, "", "", d.domain)
		if err != nil {
			return nil, fmt.Errorf("dns_srv lookup failed: %w", err)
		}
		for _, a := range addrs {
			srvs = append(srvs, &dns.SRV{Target: a.Target, Port: a.Port})
		}
	}

	var targets []*models.Target
	for _, srv := range srvs {
		host := strings.TrimSuffix(srv.Target, ".")
		addr := net.JoinHostPort(host, fmt.Sprintf("%d", srv.Port))
		targetAddr := addr
		if d.probeType == models.TargetTypeHTTP {
			scheme := "http"
			if srv.Port == 443 {
				scheme = "https"
			}
			targetAddr = fmt.Sprintf("%s://%s", scheme, addr)
		}
		name := fmt.Sprintf("dns-%s-%d", strings.ReplaceAll(host, ".", "-"), srv.Port)
		t := models.NewTarget(name, d.probeType, targetAddr, 30000, 10000)
		t.ID = name
		t.Labels["discovered_by"] = "dns_srv"
		t.Labels["srv_domain"] = d.domain
		targets = append(targets, t)
	}
	return targets, nil
}
