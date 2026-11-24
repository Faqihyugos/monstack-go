package scheduler_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/internal/scheduler"
	"github.com/Faqihyugos/monstack-go/internal/storage/meta"
	"github.com/Faqihyugos/monstack-go/pkg/models"
	"github.com/Faqihyugos/monstack-go/tests/e2e/harness"
)

func TestWorkerPoolConcurrency(t *testing.T) {
	pool := scheduler.NewWorkerPool(4, 16)
	defer pool.Close()

	var count atomic.Int32
	var maxActive atomic.Int32
	var active atomic.Int32

	ctx := context.Background()
	for i := 0; i < 20; i++ {
		ok := pool.Submit(ctx, func(ctx context.Context) {
			cur := active.Add(1)
			for {
				old := maxActive.Load()
				if cur <= old || maxActive.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			active.Add(-1)
			count.Add(1)
		})
		if !ok {
			t.Fatalf("Submit failed at %d", i)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for count.Load() < 20 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count.Load() != 20 {
		t.Fatalf("expected 20 completed jobs, got %d", count.Load())
	}
	if maxActive.Load() > 4 {
		t.Fatalf("expected max concurrency <= 4, got %d", maxActive.Load())
	}
}

func TestSchedulerLifecycleAndDynamicTargets(t *testing.T) {
	dir := t.TempDir()
	db, err := meta.Open(config.MetadataConfig{
		Path:        filepath.Join(dir, "meta.db"),
		WALMode:     true,
		BusyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("meta.Open: %v", err)
	}
	defer db.Close()

	mockHTTP := harness.NewMockHTTPServer()
	defer mockHTTP.Close()

	ctx := context.Background()
	enabledTarget := models.NewTarget("svc-1", models.TargetTypeHTTP, mockHTTP.URL()+"/ok", 80, 50)
	enabledTarget.ID = "svc-1"
	if err := db.CreateTarget(ctx, enabledTarget); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	disabledTarget := models.NewTarget("svc-disabled", models.TargetTypeHTTP, mockHTTP.URL()+"/disabled", 80, 50)
	disabledTarget.ID = "svc-disabled"
	disabledTarget.Enabled = false
	if err := db.CreateTarget(ctx, disabledTarget); err != nil {
		t.Fatalf("CreateTarget disabled: %v", err)
	}

	engine := prober.NewDefaultEngine()
	sched := scheduler.NewScheduler(db, engine, 4, nil, nil)
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("sched.Start: %v", err)
	}
	defer sched.Stop()

	if !harness.PollUntil(2*time.Second, 20*time.Millisecond, func() bool {
		return mockHTTP.RequestCount() >= 2
	}) {
		t.Fatalf("expected >= 2 probe requests, got %d", mockHTTP.RequestCount())
	}

	if sched.ActiveCount() != 1 {
		t.Fatalf("expected 1 active target (disabled skipped), got %d", sched.ActiveCount())
	}

	// Remove target and verify probes stop
	_ = db.DeleteTarget(ctx, "svc-1")
	sched.RemoveTarget("svc-1")
	countAtDelete := mockHTTP.RequestCount()
	time.Sleep(180 * time.Millisecond)
	if mockHTTP.RequestCount() > countAtDelete+1 {
		t.Fatalf("expected probes to stop after removal, before=%d after=%d", countAtDelete, mockHTTP.RequestCount())
	}
}
