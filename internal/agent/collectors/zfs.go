package collectors

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// ZFSCollector collects ZFS pool capacity and health metrics by parsing `zpool list -Hp`.
type ZFSCollector struct{}

func NewZFSCollector() *ZFSCollector { return &ZFSCollector{} }
func (z *ZFSCollector) Name() string { return "zfs" }

func (z *ZFSCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	if _, err := exec.LookPath("zpool"); err != nil {
		// ZFS not installed on this host; graceful no-op
		return nil, nil
	}

	out, err := exec.CommandContext(ctx, "zpool", "list", "-Hp", "-o", "name,size,alloc,free,cap,health").Output()
	if err != nil {
		return nil, err
	}
	return ParseZPoolOutput(agentID, string(out), time.Now().UTC()), nil
}

// ParseZPoolOutput parses tab-separated `zpool list -Hp -o name,size,alloc,free,cap,health` output.
func ParseZPoolOutput(agentID, output string, now time.Time) []models.MetricPoint {
	var pts []models.MetricPoint
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		pool := fields[0]
		labels := map[string]string{"pool": pool}

		if size, err := strconv.ParseFloat(fields[1], 64); err == nil {
			pts = append(pts, models.MetricPoint{TargetID: agentID, Metric: "agent_zfs_pool_size_bytes", Value: size, Timestamp: now, Labels: labels})
		}
		if alloc, err := strconv.ParseFloat(fields[2], 64); err == nil {
			pts = append(pts, models.MetricPoint{TargetID: agentID, Metric: "agent_zfs_pool_allocated_bytes", Value: alloc, Timestamp: now, Labels: labels})
		}
		if free, err := strconv.ParseFloat(fields[3], 64); err == nil {
			pts = append(pts, models.MetricPoint{TargetID: agentID, Metric: "agent_zfs_pool_free_bytes", Value: free, Timestamp: now, Labels: labels})
		}
		capStr := strings.TrimSuffix(fields[4], "%")
		if capPct, err := strconv.ParseFloat(capStr, 64); err == nil {
			pts = append(pts, models.MetricPoint{TargetID: agentID, Metric: "agent_zfs_pool_capacity_percent", Value: capPct, Timestamp: now, Labels: labels})
		}
		healthVal := 0.0
		if strings.EqualFold(fields[5], "ONLINE") {
			healthVal = 1.0
		}
		pts = append(pts, models.MetricPoint{TargetID: agentID, Metric: "agent_zfs_pool_online", Value: healthVal, Timestamp: now, Labels: labels})
	}
	return pts
}
