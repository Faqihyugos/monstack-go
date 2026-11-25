package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// CIDRDiscoverer scans configured CIDR subnets for open TCP ports and registers found endpoints.
type CIDRDiscoverer struct {
	networks    []string
	ports       []int
	probeType   models.TargetType
	interval    time.Duration
	dialTimeout time.Duration
	concurrency int
	logger      *slog.Logger
}

// NewCIDRDiscoverer creates a CIDR network scanner from configuration.
func NewCIDRDiscoverer(cfg map[string]any, logger *slog.Logger) (*CIDRDiscoverer, error) {
	var networks []string
	if rawNets, ok := cfg["networks"].([]any); ok {
		for _, item := range rawNets {
			if s := strings.TrimSpace(fmt.Sprintf("%v", item)); s != "" {
				networks = append(networks, s)
			}
		}
	} else if rawNetStrs, ok := cfg["networks"].([]string); ok {
		networks = append(networks, rawNetStrs...)
	}
	if len(networks) == 0 {
		return nil, errors.New("cidr_scan discovery requires at least one CIDR in 'networks'")
	}

	var ports []int
	if rawPorts, ok := cfg["ports"].([]any); ok {
		for _, item := range rawPorts {
			switch pv := item.(type) {
			case int:
				ports = append(ports, pv)
			case float64:
				ports = append(ports, int(pv))
			case string:
				if pn, err := strconv.Atoi(pv); err == nil {
					ports = append(ports, pn)
				}
			}
		}
	} else if rawPortInts, ok := cfg["ports"].([]int); ok {
		ports = append(ports, rawPortInts...)
	}
	if len(ports) == 0 {
		ports = []int{80, 443}
	}

	probeType := models.TargetType(strings.ToLower(getStr(cfg, "probe_type")))
	if !probeType.IsValid() {
		probeType = models.TargetTypeTCP
	}

	return &CIDRDiscoverer{
		networks:    networks,
		ports:       ports,
		probeType:   probeType,
		interval:    getDuration(cfg, 30*time.Minute, "interval", "refresh_interval"),
		dialTimeout: getDuration(cfg, 500*time.Millisecond, "dial_timeout", "timeout"),
		concurrency: 32,
		logger:      logger,
	}, nil
}

func (c *CIDRDiscoverer) Name() string            { return "cidr_scan" }
func (c *CIDRDiscoverer) Interval() time.Duration { return c.interval }

// Discover sweeps the configured CIDR ranges and ports with bounded concurrency.
func (c *CIDRDiscoverer) Discover(ctx context.Context) ([]*models.Target, error) {
	type probeAddr struct {
		ip   string
		port int
	}

	jobs := make(chan probeAddr, c.concurrency*2)
	var (
		mu      sync.Mutex
		targets []*models.Target
		wg      sync.WaitGroup
	)

	for i := 0; i < c.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialer := &net.Dialer{Timeout: c.dialTimeout}
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				addr := net.JoinHostPort(job.ip, strconv.Itoa(job.port))
				conn, err := dialer.DialContext(ctx, "tcp", addr)
				if err != nil {
					continue
				}
				_ = conn.Close()

				targetAddr := addr
				if c.probeType == models.TargetTypeHTTP {
					scheme := "http"
					if job.port == 443 {
						scheme = "https"
					}
					targetAddr = fmt.Sprintf("%s://%s", scheme, addr)
				}
				name := fmt.Sprintf("cidr-%s-%d", strings.ReplaceAll(job.ip, ".", "-"), job.port)
				t := models.NewTarget(name, c.probeType, targetAddr, 30000, 10000)
				t.ID = name
				t.Labels["discovered_by"] = "cidr_scan"

				mu.Lock()
				targets = append(targets, t)
				mu.Unlock()
			}
		}()
	}

	for _, cidr := range c.networks {
		ips, err := expandCIDR(cidr, 1024)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			for _, port := range c.ports {
				select {
				case <-ctx.Done():
					close(jobs)
					wg.Wait()
					return targets, ctx.Err()
				case jobs <- probeAddr{ip: ip, port: port}:
				}
			}
		}
	}

	close(jobs)
	wg.Wait()
	return targets, nil
}

func expandCIDR(cidr string, maxHosts int) ([]string, error) {
	// Support single IP without /mask
	if !strings.Contains(cidr, "/") {
		if ip := net.ParseIP(cidr); ip != nil {
			return []string{ip.String()}, nil
		}
		return nil, fmt.Errorf("invalid IP/CIDR: %q", cidr)
	}

	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	var ips []string
	for cur := ip.Mask(ipNet.Mask); ipNet.Contains(cur); incIP(cur) {
		ips = append(ips, cur.String())
		if len(ips) >= maxHosts {
			break
		}
	}
	// Strip network & broadcast addresses for subnets larger than /31
	if len(ips) > 2 {
		return ips[1 : len(ips)-1], nil
	}
	return ips, nil
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
