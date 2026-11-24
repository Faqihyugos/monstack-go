package alerting_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/alerting"
	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/internal/storage/meta"
	"github.com/Faqihyugos/monstack-go/internal/storage/tsdb"
	"github.com/Faqihyugos/monstack-go/pkg/models"
	"github.com/Faqihyugos/monstack-go/tests/e2e/harness"
)

func TestParseExpression(t *testing.T) {
	cases := []struct {
		expr      string
		metric    string
		op        string
		threshold float64
		val       float64
		want      bool
	}{
		{"probe_success == 0", "probe_success", "==", 0, 0, true},
		{"probe_success == 0", "probe_success", "==", 0, 1, false},
		{"probe_response_time > 2s", "probe_response_time", ">", 2.0, 2.5, true},
		{"probe_response_time > 500ms", "probe_response_time", ">", 0.5, 0.3, false},
		{"probe_ssl_days_remaining < 14", "probe_ssl_days_remaining", "<", 14, 7, true},
		{"probe_http_status_code != 200", "probe_http_status_code", "!=", 200, 500, true},
		{"latency >= 1.5", "latency", ">=", 1.5, 1.5, true},
		{"latency <= 1.5", "latency", "<=", 1.5, 1.6, false},
	}

	for _, tc := range cases {
		p, err := alerting.ParseExpression(tc.expr)
		if err != nil {
			t.Fatalf("ParseExpression(%q) error: %v", tc.expr, err)
		}
		if p.Metric != tc.metric || p.Operator != tc.op || p.Threshold != tc.threshold {
			t.Errorf("ParseExpression(%q) = %+v, want metric=%s op=%s threshold=%v", tc.expr, p, tc.metric, tc.op, tc.threshold)
		}
		if got := p.Evaluate(tc.val); got != tc.want {
			t.Errorf("Evaluate(%v) for %q = %v, want %v", tc.val, tc.expr, got, tc.want)
		}
	}
}

func TestEvaluatorLifecycleAndTelegram(t *testing.T) {
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

	mockTG := harness.NewMockTelegramServer()
	defer mockTG.Close()

	ctx := context.Background()
	tgt := models.NewTarget("api-1", models.TargetTypeHTTP, "http://127.0.0.1:8080/health", 1000, 500)
	tgt.ID = "api-1"
	if err := db.CreateTarget(ctx, tgt); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	rule := &models.AlertRule{
		ID:          "rule-down",
		Name:        "Target Down",
		Expression:  "probe_success == 0",
		ForDuration: "50ms",
		Severity:    models.SeverityCritical,
		Enabled:     true,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := db.CreateAlertRule(ctx, rule); err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	alertCfg := config.AlertingConfig{
		Channels: []config.AlertChannelConfig{
			{
				Name: "telegram",
				Type: "telegram",
				Config: map[string]any{
					"token":   "123:ABC",
					"chat_id": "999",
					"url":     mockTG.URL() + "/bot123:ABC/sendMessage",
				},
			},
		},
	}

	router := alerting.NewRouter(alertCfg, nil, nil)
	silencer := alerting.NewSilencer(db, nil)
	engine := prober.NewDefaultEngine(prober.WithTSDB(ts))
	eval := alerting.NewEvaluator(db, ts, engine, router, silencer, nil, 50*time.Millisecond, nil)

	// Write failing metric point -> first eval should be PENDING (for=50ms)
	_ = ts.WritePoint(models.MetricPoint{
		TargetID:  tgt.ID,
		Metric:    models.MetricProbeSuccess,
		Value:     0,
		Timestamp: time.Now().UTC(),
	})
	eval.EvaluateOnce(ctx)

	firing, _ := db.ListAlertEvents(ctx, string(models.AlertStatusFiring), 10)
	if len(firing) != 0 {
		t.Fatalf("expected 0 firing alerts during pending window, got %d", len(firing))
	}

	// Wait past 'for' duration -> should transition to FIRING and send Telegram
	time.Sleep(60 * time.Millisecond)
	eval.EvaluateOnce(ctx)

	firing, _ = db.ListAlertEvents(ctx, string(models.AlertStatusFiring), 10)
	if len(firing) != 1 {
		t.Fatalf("expected 1 firing alert, got %d", len(firing))
	}

	msg, err := mockTG.WaitForMessage(2 * time.Second)
	if err != nil {
		t.Fatalf("expected Telegram message: %v", err)
	}
	if !strings.Contains(msg.Text, "CRITICAL") || !strings.Contains(msg.Text, "Target Down") {
		t.Errorf("unexpected Telegram firing text: %s", msg.Text)
	}

	// Recover -> should transition to RESOLVED and send second Telegram message
	_ = ts.WritePoint(models.MetricPoint{
		TargetID:  tgt.ID,
		Metric:    models.MetricProbeSuccess,
		Value:     1,
		Timestamp: time.Now().UTC(),
	})
	eval.EvaluateOnce(ctx)

	firing, _ = db.ListAlertEvents(ctx, string(models.AlertStatusFiring), 10)
	if len(firing) != 0 {
		t.Fatalf("expected 0 firing alerts after recovery, got %d", len(firing))
	}

	msgs, err := mockTG.WaitForMessages(2, 2*time.Second)
	if err != nil {
		t.Fatalf("expected resolution Telegram message: %v", err)
	}
	if !strings.Contains(strings.ToLower(msgs[1].Text), "resolved") {
		t.Errorf("expected resolved message, got: %s", msgs[1].Text)
	}
}

func TestEvaluatorSilenceSuppression(t *testing.T) {
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

	mockTG := harness.NewMockTelegramServer()
	defer mockTG.Close()

	ctx := context.Background()
	tgt := models.NewTarget("maint-node", models.TargetTypeHTTP, "http://127.0.0.1:8080/health", 1000, 500)
	tgt.ID = "maint-node"
	_ = db.CreateTarget(ctx, tgt)

	rule := &models.AlertRule{
		ID:          "rule-maint",
		Name:        "Down",
		Expression:  "probe_success == 0",
		ForDuration: "0s",
		Severity:    models.SeverityCritical,
		Enabled:     true,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	_ = db.CreateAlertRule(ctx, rule)

	sil := &models.AlertSilence{
		ID:        "sil-1",
		Matchers:  `{"target":"maint-node"}`,
		StartsAt:  time.Now().UTC().Add(-10 * time.Minute),
		EndsAt:    time.Now().UTC().Add(10 * time.Minute),
		Comment:   "planned maintenance",
		CreatedBy: "ops",
		CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateSilence(ctx, sil); err != nil {
		t.Fatalf("CreateSilence: %v", err)
	}

	alertCfg := config.AlertingConfig{
		Channels: []config.AlertChannelConfig{
			{
				Name: "telegram",
				Type: "telegram",
				Config: map[string]any{
					"token":   "123:ABC",
					"chat_id": "999",
					"url":     mockTG.URL() + "/bot123:ABC/sendMessage",
				},
			},
		},
	}

	router := alerting.NewRouter(alertCfg, nil, nil)
	silencer := alerting.NewSilencer(db, nil)
	eval := alerting.NewEvaluator(db, ts, nil, router, silencer, nil, 50*time.Millisecond, nil)

	_ = ts.WritePoint(models.MetricPoint{
		TargetID:  tgt.ID,
		Metric:    models.MetricProbeSuccess,
		Value:     0,
		Timestamp: time.Now().UTC(),
	})
	eval.EvaluateOnce(ctx)

	if mockTG.MessageCount() != 0 {
		t.Fatalf("expected 0 Telegram messages when silenced, got %d", mockTG.MessageCount())
	}
}
