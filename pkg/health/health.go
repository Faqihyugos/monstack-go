package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Status represents the health condition of a component or the overall system.
type Status string

const (
	StatusUP       Status = "UP"
	StatusDOWN     Status = "DOWN"
	StatusDEGRADED Status = "DEGRADED"
)

// ComponentStatus captures individual subsystem health state and details.
type ComponentStatus struct {
	Status    Status         `json:"status"`
	Details   map[string]any `json:"details,omitempty"`
	Error     string         `json:"error,omitempty"`
	CheckedAt time.Time      `json:"checked_at"`
}

// CheckFunc is a callback invoked to check the health of a registered component.
type CheckFunc func(ctx context.Context) ComponentStatus

// SystemHealth aggregates the health status of all registered components.
type SystemHealth struct {
	Status        Status                     `json:"status"`
	Version       string                     `json:"version,omitempty"`
	Uptime        string                     `json:"uptime,omitempty"`
	UptimeSeconds float64                    `json:"uptime_seconds"`
	Timestamp     time.Time                  `json:"timestamp"`
	Components    map[string]ComponentStatus `json:"components"`
}

// Registry manages thread-safe component health check registration and execution.
type Registry struct {
	mu        sync.RWMutex
	checkers  map[string]CheckFunc
	startTime time.Time
	version   string
}

// NewRegistry initializes a new health check Registry.
func NewRegistry(version string) *Registry {
	return &Registry{
		checkers:  make(map[string]CheckFunc),
		startTime: time.Now().UTC(),
		version:   version,
	}
}

// SetVersion sets the application version reported in system health.
func (r *Registry) SetVersion(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version = v
}

// Register adds a named component check function to the registry.
func (r *Registry) Register(name string, check CheckFunc) error {
	if name == "" {
		return errors.New("component name cannot be empty")
	}
	if check == nil {
		return errors.New("check function cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkers[name] = check
	return nil
}

// Unregister removes a component health check from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checkers, name)
}

// Check evaluates all registered component health checks concurrently with panic protection.
func (r *Registry) Check(ctx context.Context) SystemHealth {
	r.mu.RLock()
	checkers := make(map[string]CheckFunc, len(r.checkers))
	for k, v := range r.checkers {
		checkers[k] = v
	}
	startTime := r.startTime
	version := r.version
	r.mu.RUnlock()

	now := time.Now().UTC()
	uptime := now.Sub(startTime)
	components := make(map[string]ComponentStatus, len(checkers))

	var wg sync.WaitGroup
	var compMu sync.Mutex

	for name, fn := range checkers {
		wg.Add(1)
		go func(cName string, checkFn CheckFunc) {
			defer wg.Done()
			status := executeSafeCheck(ctx, checkFn)
			compMu.Lock()
			components[cName] = status
			compMu.Unlock()
		}(name, fn)
	}

	wg.Wait()

	overallStatus := StatusUP
	hasDegraded := false
	for _, cs := range components {
		if cs.Status == StatusDOWN {
			overallStatus = StatusDOWN
			break
		}
		if cs.Status == StatusDEGRADED {
			hasDegraded = true
		}
	}
	if overallStatus != StatusDOWN && hasDegraded {
		overallStatus = StatusDEGRADED
	}

	return SystemHealth{
		Status:        overallStatus,
		Version:       version,
		Uptime:        uptime.Truncate(time.Second).String(),
		UptimeSeconds: uptime.Seconds(),
		Timestamp:     now,
		Components:    components,
	}
}

// executeSafeCheck runs a check function catching any unexpected panics.
func executeSafeCheck(ctx context.Context, fn CheckFunc) (status ComponentStatus) {
	defer func() {
		if r := recover(); r != nil {
			status = ComponentStatus{
				Status:    StatusDOWN,
				Error:     fmt.Sprintf("health check panic: %v", r),
				CheckedAt: time.Now().UTC(),
			}
		}
	}()
	return fn(ctx)
}

// HTTPHandler returns an http.HandlerFunc serving the JSON SystemHealth report.
func (r *Registry) HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		report := r.Check(req.Context())
		w.Header().Set("Content-Type", "application/json")
		if report.Status == StatusDOWN {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(report)
	}
}

// Global default singleton registry.
var defaultRegistry = NewRegistry("dev")

// Register registers a check with the default registry.
func Register(name string, check CheckFunc) error { return defaultRegistry.Register(name, check) }

// Unregister removes a check from the default registry.
func Unregister(name string) { defaultRegistry.Unregister(name) }

// Check runs all checks in the default registry.
func Check(ctx context.Context) SystemHealth { return defaultRegistry.Check(ctx) }

// Handler returns the HTTP handler for the default registry.
func Handler() http.HandlerFunc { return defaultRegistry.HTTPHandler() }

// SetVersion sets the version for the default registry.
func SetVersion(v string) { defaultRegistry.SetVersion(v) }
