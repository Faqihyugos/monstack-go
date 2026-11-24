package collectors

import (
	"context"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Collector gathers host or service metrics on an agent node.
type Collector interface {
	Name() string
	Collect(ctx context.Context, agentID string) ([]models.MetricPoint, error)
}
