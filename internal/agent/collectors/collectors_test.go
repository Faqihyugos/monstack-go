package collectors_test

import (
	"context"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/agent/collectors"
)

func TestSystemCollectors(t *testing.T) {
	ctx := context.Background()
	cols := []collectors.Collector{
		collectors.NewCPUCollector(),
		collectors.NewMemoryCollector(),
		collectors.NewDiskCollector([]string{"/"}),
		collectors.NewNetworkCollector(),
		collectors.NewLoadCollector(),
	}

	for _, col := range cols {
		pts, err := col.Collect(ctx, "agent-test")
		if err != nil {
			t.Fatalf("collector %s failed: %v", col.Name(), err)
		}
		if len(pts) == 0 {
			t.Errorf("collector %s returned 0 metric points", col.Name())
		}
	}
}

func TestParseZPoolOutput(t *testing.T) {
	sample := "tank\t1000000000\t250000000\t750000000\t25%\tONLINE\nbackup\t2000000000\t1900000000\t100000000\t95%\tDEGRADED\n"
	pts := collectors.ParseZPoolOutput("agent-zfs", sample, time.Now().UTC())
	if len(pts) != 10 {
		t.Fatalf("expected 10 ZFS metric points (5 per pool), got %d", len(pts))
	}
}

func TestParseBIRDProtocols(t *testing.T) {
	sample := `BIRD 2.0.8 ready.
Name       Proto      Table      State  Since         Info
device1    Device     ---        up     2026-01-01
bgp_ix     BGP        master4    up     2026-01-01    Established
bgp_peer2  BGP        master4    start  2026-01-01    Connect
`
	pts := collectors.ParseBIRDProtocols("agent-bird", sample, time.Now().UTC())
	if len(pts) != 3 {
		t.Fatalf("expected 3 BIRD protocol points, got %d", len(pts))
	}
	if pts[1].Value != 1.0 || pts[2].Value != 0.0 {
		t.Errorf("unexpected BIRD up states: bgp_ix=%v bgp_peer2=%v", pts[1].Value, pts[2].Value)
	}
}
