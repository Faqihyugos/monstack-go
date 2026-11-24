package prober

import (
	"sync"

	"github.com/yourusername/monstack-go/pkg/models"
)

// ProbeType is an alias for models.TargetType to provide type consistency.
type ProbeType = models.TargetType

// Registry defines a thread-safe registry mapping target/probe types to Prober instances.
type Registry interface {
	Register(probeType models.TargetType, p Prober)
	Get(probeType models.TargetType) (Prober, bool)
	List() []models.TargetType
}

// DefaultRegistry is the thread-safe default implementation of Registry.
type DefaultRegistry struct {
	mu      sync.RWMutex
	probers map[models.TargetType]Prober
}

// NewRegistry creates a new, empty DefaultRegistry.
func NewRegistry() *DefaultRegistry {
	return &DefaultRegistry{
		probers: make(map[models.TargetType]Prober),
	}
}

// Register registers a Prober for the given target type. If a prober already exists
// for the type, it is overwritten.
func (r *DefaultRegistry) Register(probeType models.TargetType, p Prober) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probers[probeType] = p
}

// Get looks up a Prober by target type.
func (r *DefaultRegistry) Get(probeType models.TargetType) (Prober, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.probers[probeType]
	return p, ok
}

// List returns a slice of all registered target types.
func (r *DefaultRegistry) List() []models.TargetType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]models.TargetType, 0, len(r.probers))
	for t := range r.probers {
		types = append(types, t)
	}
	return types
}
