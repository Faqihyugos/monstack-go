package prober_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/pkg/models"
	"github.com/Faqihyugos/monstack-go/tests/e2e/harness"
)

func TestUDPProber(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if string(buf[:n]) == "PING" {
				_, _ = pc.WriteTo([]byte("PONG"), addr)
			}
		}
	}()

	p := prober.NewUDPProber()
	tgt := models.NewTarget("udp-echo", models.TargetTypeUDP, pc.LocalAddr().String(), 5000, 2000)
	tgt.ID = "udp-echo"
	tgt.Config = map[string]any{
		"send":   "PING",
		"expect": "PONG",
	}

	res, err := p.Probe(context.Background(), tgt)
	if err != nil {
		t.Fatalf("UDP Probe error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected UDP probe success, got error: %s", res.Error)
	}
}

func TestDNSProber(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}

	mux := dns.NewServeMux()
	mux.HandleFunc("monstack.local.", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		rr, _ := dns.NewRR("monstack.local. 60 IN A 10.20.30.40")
		m.Answer = append(m.Answer, rr)
		_ = w.WriteMsg(m)
	})

	srv := &dns.Server{PacketConn: pc, Handler: mux}
	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() { _ = srv.ActivateAndServe() }()
	<-ready
	defer func() { _ = srv.Shutdown() }()

	p := prober.NewDNSProber()
	tgt := models.NewTarget("dns-local", models.TargetTypeDNS, "monstack.local", 5000, 2000)
	tgt.ID = "dns-local"
	tgt.Config = map[string]any{
		"server":        pc.LocalAddr().String(),
		"record_type":   "A",
		"expect_answer": "10.20.30.40",
	}

	res, err := p.Probe(context.Background(), tgt)
	if err != nil {
		t.Fatalf("DNS Probe error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected DNS probe success, got error: %s", res.Error)
	}
}

func TestTLSProber(t *testing.T) {
	mockHTTPS, err := harness.NewMockHTTPSServer(harness.TLSCertOptions{
		CommonName: "127.0.0.1",
		ValidFor:   30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewMockHTTPSServer: %v", err)
	}
	defer mockHTTPS.Close()

	// Strip https:// prefix to get host:port
	addr := mockHTTPS.URL()[len("https://"):]

	p := prober.NewTLSProber()
	tgt := models.NewTarget("tls-check", models.TargetTypeTLS, addr, 5000, 2000)
	tgt.ID = "tls-check"
	tgt.Config = map[string]any{"skip_verify": true}

	res, err := p.Probe(context.Background(), tgt)
	if err != nil {
		t.Fatalf("TLS Probe error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected TLS probe success, got error: %s", res.Error)
	}

	foundDays := false
	for _, m := range res.Metrics {
		if m.Metric == models.MetricHTTPSSLExpiryDays && m.Value > 28 && m.Value < 31 {
			foundDays = true
		}
	}
	if !foundDays {
		t.Fatalf("expected ~30 ssl_days_remaining metric, got %+v", res.Metrics)
	}
}

func TestGRPCProber(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	hs := health.NewServer()
	hs.SetServingStatus("monstack.api", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcSrv, hs)
	go func() { _ = grpcSrv.Serve(ln) }()
	defer grpcSrv.Stop()

	p := prober.NewGRPCProber()
	tgt := models.NewTarget("grpc-api", models.TargetTypeGRPC, ln.Addr().String(), 5000, 2000)
	tgt.ID = "grpc-api"
	tgt.Config = map[string]any{
		"service": "monstack.api",
		"tls":     false,
	}

	res, err := p.Probe(context.Background(), tgt)
	if err != nil {
		t.Fatalf("gRPC Probe error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected gRPC probe success, got error: %s", res.Error)
	}
}

func TestDefaultEngineHasAllPhase2Probers(t *testing.T) {
	eng := prober.NewDefaultEngine()
	for _, typ := range []models.TargetType{
		models.TargetTypeHTTP,
		models.TargetTypeICMP,
		models.TargetTypeTCP,
		models.TargetTypeUDP,
		models.TargetTypeDNS,
		models.TargetTypeTLS,
		models.TargetTypeGRPC,
		models.TargetTypeSNMP,
	} {
		if _, ok := eng.GetProber(typ); !ok {
			t.Errorf("expected prober for type %q to be registered in default engine", typ)
		}
	}
}
