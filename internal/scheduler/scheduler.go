package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

type scheduledTarget struct {
	target *models.Target
	cancel context.CancelFunc
	done   chan struct{}
}

// Scheduler manages periodic probing of all enabled targets using a bounded worker pool.
type Scheduler struct {
	meta    storage.MetadataStore
	engine  prober.Engine
	pool    *WorkerPool
	metrics *metrics.Metrics
	logger  *slog.Logger

	mu              sync.Mutex
	active          map[string]*scheduledTarget
	onProbeComplete func()
	ctx             context.Context
	cancel          context.CancelFunc
	syncDone        chan struct{}
	running         bool
	syncInt         time.Duration
}

// NewScheduler creates a new probe Scheduler.
func NewScheduler(
	meta storage.MetadataStore,
	engine prober.Engine,
	workers int,
	m *metrics.Metrics,
	logger *slog.Logger,
) *Scheduler {
	if workers <= 0 {
		workers = 16
	}
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default()
	}
	return &Scheduler{
		meta:    meta,
		engine:  engine,
		pool:    NewWorkerPool(workers, workers*8),
		metrics: m,
		logger:  logger,
		active:  make(map[string]*scheduledTarget),
		syncInt: 250 * time.Millisecond,
	}
}

// Start begins scheduling probes and periodically reconciling targets with MetadataStore.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.syncDone = make(chan struct{})
	s.running = true
	s.mu.Unlock()

	// Initial sync immediately
	s.SyncTargets(s.ctx)

	go s.syncLoop()
	return nil
}

func (s *Scheduler) syncLoop() {
	defer close(s.syncDone)
	ticker := time.NewTicker(s.syncInt)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.SyncTargets(s.ctx)
		}
	}
}

// SyncTargets reconciles active probe loops with the current targets stored in MetadataStore.
func (s *Scheduler) SyncTargets(ctx context.Context) {
	if s.meta == nil {
		return
	}

	targets, err := s.meta.ListTargets(ctx, "", nil)
	if err != nil {
		s.logger.Error("scheduler failed to list targets", "err", err)
		return
	}

	// Update target count metrics
	counts := make(map[string]map[bool]float64)
	desired := make(map[string]*models.Target, len(targets))
	for _, t := range targets {
		if counts[string(t.Type)] == nil {
			counts[string(t.Type)] = map[bool]float64{true: 0, false: 0}
		}
		counts[string(t.Type)][t.Enabled]++
		if t.Enabled {
			desired[t.ID] = t
		}
	}
	if s.metrics != nil {
		for tType, byEnabled := range counts {
			for en, c := range byEnabled {
				s.metrics.SetTargetsCount(tType, en, c)
			}
		}
	}

	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}

	// Stop removed or disabled targets
	for id, st := range s.active {
		newT, ok := desired[id]
		if !ok || targetChanged(st.target, newT) {
			st.cancel()
			delete(s.active, id)
		}
	}

	// Start new or updated enabled targets
	for id, t := range desired {
		if _, exists := s.active[id]; !exists {
			s.startTargetLocked(t)
		}
	}
	s.mu.Unlock()
}

// AddOrUpdateTarget immediately schedules or updates a single target.
func (s *Scheduler) AddOrUpdateTarget(t *models.Target) {
	if t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}

	if existing, ok := s.active[t.ID]; ok {
		existing.cancel()
		delete(s.active, t.ID)
	}
	if t.Enabled {
		s.startTargetLocked(t)
	}
}

// RemoveTarget immediately stops probing a target by ID.
func (s *Scheduler) RemoveTarget(targetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.active[targetID]; ok {
		existing.cancel()
		delete(s.active, targetID)
	}
}

func (s *Scheduler) startTargetLocked(t *models.Target) {
	tCopy := *t
	tCtx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	st := &scheduledTarget{
		target: &tCopy,
		cancel: cancel,
		done:   done,
	}
	s.active[t.ID] = st

	go func() {
		defer close(done)
		interval := time.Duration(tCopy.IntervalMs) * time.Millisecond
		if interval <= 0 {
			interval = 30 * time.Second
		}

		// Execute immediately on schedule start
		s.dispatchProbe(tCtx, &tCopy)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-tCtx.Done():
				return
			case <-ticker.C:
				s.dispatchProbe(tCtx, &tCopy)
			}
		}
	}()
}

// SetOnProbeComplete registers a callback invoked after each probe execution completes.
func (s *Scheduler) SetOnProbeComplete(fn func()) {
	s.mu.Lock()
	s.onProbeComplete = fn
	s.mu.Unlock()
}

func (s *Scheduler) dispatchProbe(ctx context.Context, target *models.Target) {
	if s.engine == nil {
		return
	}
	tCopy := *target
	s.pool.Submit(ctx, func(poolCtx context.Context) {
		if ctx.Err() != nil {
			return
		}
		_, err := s.engine.Execute(ctx, &tCopy)
		if err != nil {
			s.logger.Debug("probe execution returned error", "target", tCopy.Name, "type", tCopy.Type, "err", err)
		}
		s.mu.Lock()
		cb := s.onProbeComplete
		s.mu.Unlock()
		if cb != nil {
			cb()
		}
	})
}

// Stop gracefully stops all scheduled target loops and shuts down the worker pool.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.cancel()
	active := make([]*scheduledTarget, 0, len(s.active))
	for id, st := range s.active {
		st.cancel()
		active = append(active, st)
		delete(s.active, id)
	}
	syncDone := s.syncDone
	s.mu.Unlock()

	if syncDone != nil {
		<-syncDone
	}
	for _, st := range active {
		<-st.done
	}
	s.pool.Close()
}

// ActiveCount returns the number of currently scheduled targets.
func (s *Scheduler) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

func targetChanged(oldT, newT *models.Target) bool {
	if oldT == nil || newT == nil {
		return true
	}
	return oldT.Target != newT.Target ||
		oldT.Type != newT.Type ||
		oldT.IntervalMs != newT.IntervalMs ||
		oldT.TimeoutMs != newT.TimeoutMs ||
		oldT.Enabled != newT.Enabled ||
		!oldT.UpdatedAt.Equal(newT.UpdatedAt)
}
