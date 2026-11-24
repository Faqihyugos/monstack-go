package collector_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/agent"
	"github.com/Faqihyugos/monstack-go/internal/collector"
	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/storage/meta"
	"github.com/Faqihyugos/monstack-go/internal/storage/tsdb"
	"github.com/Faqihyugos/monstack-go/tests/e2e/harness"
)

func TestCollectorAndAgentEndToEnd(t *testing.T) {
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

	ts, err := tsdb.Open(config.TSDBConfig{
		Path:      filepath.Join(dir, "tsdb"),
		Retention: "24h",
	})
	if err != nil {
		t.Fatalf("tsdb.Open: %v", err)
	}
	defer ts.Close()

	srv := collector.NewCollectorServer("127.0.0.1:0", "v1.1.0", nil, db, ts, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()

	if !harness.PollUntil(2*time.Second, 20*time.Millisecond, func() bool {
		return srv.Addr() != ""
	}) {
		t.Fatalf("gRPC collector failed to bind address")
	}

	var cmdReceived atomic.Bool
	ag := agent.New(agent.Options{
		ServerAddr:      srv.Addr(),
		Hostname:        "worker-node-01",
		AgentID:         "agent-e2e-1",
		CollectInterval: 100 * time.Millisecond,
		Collectors:      []string{"cpu", "memory", "load"},
		Labels:          map[string]string{"env": "test"},
		OnCommand: func(cmd string, params map[string]string) {
			if cmd == "ping" {
				cmdReceived.Store(true)
			}
		},
	})

	agCtx, agCancel := context.WithCancel(ctx)
	defer agCancel()
	go func() { _ = ag.Run(agCtx) }()

	// Wait until agent connects and streams metrics into TSDB
	if !harness.PollUntil(3*time.Second, 30*time.Millisecond, func() bool {
		pt, err := ts.GetLatestPoint("agent-e2e-1", "agent_memory_used_percent")
		return err == nil && pt != nil && srv.ConnectedAgents() == 1
	}) {
		t.Fatalf("expected agent metrics in TSDB and 1 connected agent (connected=%d)", srv.ConnectedAgents())
	}

	// Verify agent persisted in SQLite
	persisted, err := db.GetAgent(ctx, "agent-e2e-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if persisted.Hostname != "worker-node-01" {
		t.Errorf("expected hostname worker-node-01, got %s", persisted.Hostname)
	}

	// Push remote command from server to agent
	if err := srv.SendCommand("agent-e2e-1", "ping", map[string]string{"seq": "1"}); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if !harness.PollUntil(2*time.Second, 20*time.Millisecond, func() bool {
		return cmdReceived.Load()
	}) {
		t.Fatalf("expected agent to receive remote 'ping' command")
	}
}
