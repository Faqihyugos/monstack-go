package collectors

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// BIRDCollector collects BIRD routing daemon protocol states via `birdc show protocols`.
type BIRDCollector struct {
	socket string
}

func NewBIRDCollector(socket string) *BIRDCollector {
	return &BIRDCollector{socket: socket}
}

func (b *BIRDCollector) Name() string { return "bird" }

func (b *BIRDCollector) Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error) {
	if _, err := exec.LookPath("birdc"); err != nil {
		// BIRD not installed on this host; graceful no-op
		return nil, nil
	}

	args := []string{"show", "protocols"}
	if b.socket != "" {
		args = append([]string{"-s", b.socket}, args...)
	}
	out, err := exec.CommandContext(ctx, "birdc", args...).Output()
	if err != nil {
		return nil, err
	}
	return ParseBIRDProtocols(agentID, string(out), time.Now().UTC()), nil
}

// ParseBIRDProtocols parses `birdc show protocols` output into per-protocol up/down metrics.
func ParseBIRDProtocols(agentID, output string, now time.Time) []models.MetricPoint {
	var pts []models.MetricPoint
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "BIRD") || strings.HasPrefix(line, "Name") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		proto := fields[1]
		state := fields[3]

		up := 0.0
		if strings.EqualFold(state, "up") {
			up = 1.0
		}
		pts = append(pts, models.MetricPoint{
			TargetID:  agentID,
			Metric:    "agent_bird_protocol_up",
			Value:     up,
			Timestamp: now,
			Labels:    map[string]string{"name": name, "proto": proto},
		})
	}
	return pts
}
