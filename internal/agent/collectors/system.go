package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// CPUCollector gathers host CPU utilization percentage.
type CPUCollector struct{}

func NewCPUCollector() *CPUCollector { return &CPUCollector{} }
func (c *CPUCollector) Name() string { return "cpu" }

func (c *CPUCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	now := time.Now().UTC()
	pcts, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return nil, err
	}
	var pts []models.MetricPoint
	if len(pcts) > 0 {
		pts = append(pts, models.MetricPoint{
			TargetID:  agentID,
			Metric:    "agent_cpu_usage_percent",
			Value:     pcts[0],
			Timestamp: now,
		})
	}
	return pts, nil
}

// MemoryCollector gathers host virtual memory statistics.
type MemoryCollector struct{}

func NewMemoryCollector() *MemoryCollector { return &MemoryCollector{} }
func (m *MemoryCollector) Name() string    { return "memory" }

func (m *MemoryCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	now := time.Now().UTC()
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return []models.MetricPoint{
		{TargetID: agentID, Metric: "agent_memory_total_bytes", Value: float64(vm.Total), Timestamp: now},
		{TargetID: agentID, Metric: "agent_memory_used_bytes", Value: float64(vm.Used), Timestamp: now},
		{TargetID: agentID, Metric: "agent_memory_available_bytes", Value: float64(vm.Available), Timestamp: now},
		{TargetID: agentID, Metric: "agent_memory_used_percent", Value: vm.UsedPercent, Timestamp: now},
	}, nil
}

// DiskCollector gathers filesystem usage for configured mount points.
type DiskCollector struct {
	mountPoints []string
}

func NewDiskCollector(mountPoints []string) *DiskCollector {
	if len(mountPoints) == 0 {
		mountPoints = []string{"/"}
	}
	return &DiskCollector{mountPoints: mountPoints}
}

func (d *DiskCollector) Name() string { return "disk" }

func (d *DiskCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	now := time.Now().UTC()
	var pts []models.MetricPoint
	for _, mp := range d.mountPoints {
		usage, err := disk.UsageWithContext(ctx, mp)
		if err != nil {
			continue
		}
		labels := map[string]string{"mount": mp}
		pts = append(pts,
			models.MetricPoint{TargetID: agentID, Metric: "agent_disk_total_bytes", Value: float64(usage.Total), Timestamp: now, Labels: labels},
			models.MetricPoint{TargetID: agentID, Metric: "agent_disk_used_bytes", Value: float64(usage.Used), Timestamp: now, Labels: labels},
			models.MetricPoint{TargetID: agentID, Metric: "agent_disk_used_percent", Value: usage.UsedPercent, Timestamp: now, Labels: labels},
		)
	}
	return pts, nil
}

// NetworkCollector gathers aggregate network interface I/O counters.
type NetworkCollector struct{}

func NewNetworkCollector() *NetworkCollector { return &NetworkCollector{} }
func (n *NetworkCollector) Name() string     { return "network" }

func (n *NetworkCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	now := time.Now().UTC()
	counters, err := net.IOCountersWithContext(ctx, false)
	if err != nil {
		return nil, err
	}
	var pts []models.MetricPoint
	if len(counters) > 0 {
		c := counters[0]
		pts = append(pts,
			models.MetricPoint{TargetID: agentID, Metric: "agent_network_bytes_sent_total", Value: float64(c.BytesSent), Timestamp: now},
			models.MetricPoint{TargetID: agentID, Metric: "agent_network_bytes_recv_total", Value: float64(c.BytesRecv), Timestamp: now},
			models.MetricPoint{TargetID: agentID, Metric: "agent_network_packets_sent_total", Value: float64(c.PacketsSent), Timestamp: now},
			models.MetricPoint{TargetID: agentID, Metric: "agent_network_packets_recv_total", Value: float64(c.PacketsRecv), Timestamp: now},
		)
	}
	return pts, nil
}

// LoadCollector gathers 1/5/15 minute load averages.
type LoadCollector struct{}

func NewLoadCollector() *LoadCollector { return &LoadCollector{} }
func (l *LoadCollector) Name() string  { return "load" }

func (l *LoadCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	now := time.Now().UTC()
	avg, err := load.AvgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return []models.MetricPoint{
		{TargetID: agentID, Metric: "agent_load1", Value: avg.Load1, Timestamp: now},
		{TargetID: agentID, Metric: "agent_load5", Value: avg.Load5, Timestamp: now},
		{TargetID: agentID, Metric: "agent_load15", Value: avg.Load15, Timestamp: now},
	}, nil
}
