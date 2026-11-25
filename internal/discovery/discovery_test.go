package discovery_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/discovery"
	"github.com/Faqihyugos/monstack-go/internal/storage/meta"
)

func TestDNSSRVDiscoverer(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}

	mux := dns.NewServeMux()
	mux.HandleFunc("_http._tcp.svc.local.", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		rr, _ := dns.NewRR("_http._tcp.svc.local. 60 IN SRV 10 10 8080 app1.svc.local.")
		m.Answer = append(m.Answer, rr)
		_ = w.WriteMsg(m)
	})

	srv := &dns.Server{PacketConn: pc, Handler: mux}
	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() { _ = srv.ActivateAndServe() }()
	<-ready
	defer func() { _ = srv.Shutdown() }()

	d, err := discovery.NewDNSSRVDiscoverer(map[string]any{
		"domain":     "_http._tcp.svc.local",
		"server":     pc.LocalAddr().String(),
		"probe_type": "http",
	}, nil)
	if err != nil {
		t.Fatalf("NewDNSSRVDiscoverer: %v", err)
	}

	targets, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(targets) != 1 || targets[0].Target != "http://app1.svc.local:8080" {
		t.Fatalf("unexpected discovered targets: %+v", targets)
	}
}

func TestCIDRDiscovererAndManager(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	db, err := meta.Open(config.MetadataConfig{
		Path:        filepath.Join(dir, "meta.db"),
		WALMode:     true,
		BusyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("meta.Open: %v", err)
	}
	defer db.Close()

	rules := []config.DiscoveryRule{
		{
			Type: "cidr_scan",
			Config: map[string]any{
				"networks":     []string{"127.0.0.1/32"},
				"ports":        []int{port},
				"probe_type":   "tcp",
				"dial_timeout": "200ms",
			},
		},
	}

	mgr := discovery.NewManager(rules, db, nil, nil)
	d, err := discovery.NewDiscoverer(rules[0], nil)
	if err != nil {
		t.Fatalf("NewDiscoverer: %v", err)
	}

	synced := mgr.RunOnce(context.Background(), d)
	if synced != 1 {
		t.Fatalf("expected 1 synced target from CIDR scan, got %d", synced)
	}

	listed, err := db.ListTargets(context.Background(), "tcp", nil)
	if err != nil || len(listed) != 1 {
		t.Fatalf("expected 1 TCP target persisted in SQLite, got %d (err=%v)", len(listed), err)
	}
}

func TestStaticFileDiscoverer(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "targets.yaml")
	content := `- name: web-01
  type: http
  target: https://example.com
  interval_ms: 15000
  timeout_ms: 5000
  labels:
    env: prod
`
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d, err := discovery.NewStaticFileDiscoverer(map[string]any{"path": f}, nil)
	if err != nil {
		t.Fatalf("NewStaticFileDiscoverer: %v", err)
	}
	targets, err := d.Discover(context.Background())
	if err != nil || len(targets) != 1 {
		t.Fatalf("expected 1 target from static file, got %d (err=%v)", len(targets), err)
	}
}
