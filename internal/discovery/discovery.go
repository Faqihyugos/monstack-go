package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Discoverer locates monitoring targets from an external or dynamic source.
type Discoverer interface {
	Name() string
	Interval() time.Duration
	Discover(ctx context.Context) ([]*models.Target, error)
}

// Manager runs configured Discoverers periodically and syncs found targets into MetadataStore.
type Manager struct {
	meta        storage.MetadataStore
	discoverers []Discoverer
	onDiscover  func(*models.Target)
	logger      *slog.Logger

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewManager constructs a Discovery Manager from config.DiscoveryRule entries.
func NewManager(
	rules []config.DiscoveryRule,
	meta storage.MetadataStore,
	onDiscover func(*models.Target),
	logger *slog.Logger,
) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	var discs []Discoverer
	for _, r := range rules {
		d, err := NewDiscoverer(r, logger)
		if err != nil {
			logger.Warn("skipping invalid discovery rule", "type", r.Type, "err", err)
			continue
		}
		discs = append(discs, d)
	}
	return &Manager{
		meta:        meta,
		discoverers: discs,
		onDiscover:  onDiscover,
		logger:      logger,
	}
}

// NewDiscoverer instantiates a Discoverer from a config rule.
func NewDiscoverer(rule config.DiscoveryRule, logger *slog.Logger) (Discoverer, error) {
	switch strings.ToLower(strings.TrimSpace(rule.Type)) {
	case "dns_srv", "dns":
		return NewDNSSRVDiscoverer(rule.Config, logger)
	case "cidr_scan", "cidr":
		return NewCIDRDiscoverer(rule.Config, logger)
	case "static", "file":
		return NewStaticFileDiscoverer(rule.Config, logger)
	default:
		return nil, fmt.Errorf("unsupported discovery type: %q", rule.Type)
	}
}

// Start launches background discovery loops for all configured discoverers.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running || len(m.discoverers) == 0 {
		m.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.running = true
	m.mu.Unlock()

	for _, d := range m.discoverers {
		disc := d
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runOne(runCtx, disc)
		}()
	}
}

func (m *Manager) runOne(ctx context.Context, d Discoverer) {
	m.RunOnce(ctx, d)

	interval := d.Interval()
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RunOnce(ctx, d)
		}
	}
}

// RunOnce executes a single discovery pass and upserts discovered targets into MetadataStore.
func (m *Manager) RunOnce(ctx context.Context, d Discoverer) int {
	targets, err := d.Discover(ctx)
	if err != nil {
		m.logger.Warn("discovery pass failed", "discoverer", d.Name(), "err", err)
		return 0
	}

	synced := 0
	for _, tgt := range targets {
		if m.meta == nil {
			continue
		}
		existing, err := m.meta.GetTargetByName(ctx, tgt.Name)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				if err := m.meta.CreateTarget(ctx, tgt); err == nil {
					synced++
					if m.onDiscover != nil {
						m.onDiscover(tgt)
					}
				}
			}
			continue
		}
		if existing.Target != tgt.Target || existing.Type != tgt.Type {
			tgt.ID = existing.ID
			tgt.CreatedAt = existing.CreatedAt
			if err := m.meta.UpdateTarget(ctx, tgt); err == nil {
				synced++
				if m.onDiscover != nil {
					m.onDiscover(tgt)
				}
			}
		}
	}
	if synced > 0 {
		m.logger.Info("discovery synced targets", "discoverer", d.Name(), "count", synced)
	}
	return synced
}

// Stop terminates all discovery goroutines and waits for them to exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

func getStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func getDuration(m map[string]any, fallback time.Duration, keys ...string) time.Duration {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch dv := v.(type) {
			case time.Duration:
				if dv > 0 {
					return dv
				}
			case string:
				if d, err := time.ParseDuration(strings.TrimSpace(dv)); err == nil && d > 0 {
					return d
				}
			}
		}
	}
	return fallback
}
