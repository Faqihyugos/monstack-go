package scheduler

import (
	"context"
	"sync"
)

// Job represents a unit of work executed by the WorkerPool.
type Job func(ctx context.Context)

// WorkerPool executes submitted jobs using a bounded number of worker goroutines.
type WorkerPool struct {
	workers int
	jobs    chan Job
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.RWMutex
	closed  bool
}

// NewWorkerPool creates and starts a bounded WorkerPool with the given worker count and queue size.
func NewWorkerPool(workers, queueSize int) *WorkerPool {
	if workers <= 0 {
		workers = 16
	}
	if queueSize <= 0 {
		queueSize = workers * 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &WorkerPool{
		workers: workers,
		jobs:    make(chan Job, queueSize),
		ctx:     ctx,
		cancel:  cancel,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *WorkerPool) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job, ok := <-p.jobs:
			if !ok {
				return
			}
			if job != nil {
				job(p.ctx)
			}
		}
	}
}

// Submit enqueues a job for execution. Returns false if the pool is closed or ctx is cancelled.
func (p *WorkerPool) Submit(ctx context.Context, job Job) bool {
	if job == nil {
		return false
	}
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return false
	}
	select {
	case p.jobs <- job:
		p.mu.RUnlock()
		return true
	case <-ctx.Done():
		p.mu.RUnlock()
		return false
	case <-p.ctx.Done():
		p.mu.RUnlock()
		return false
	}
}

// Close stops accepting new jobs, cancels running workers, and waits for them to exit.
func (p *WorkerPool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.cancel()
	close(p.jobs)
	p.mu.Unlock()
	p.wg.Wait()
}

// Workers returns the configured concurrency bound.
func (p *WorkerPool) Workers() int {
	return p.workers
}
