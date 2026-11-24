package tier1_features_test

import (
	"database/sql"
	"net/http"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/yourusername/monstack-go/pkg/models"
)

// TestStorageMetadata tests SQLite schema migrations, target CRUD, and alert rules persistence.
func TestStorageMetadata(t *testing.T) {
	t.Run("MigrationsApplication", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// Open SQLite database file (meta.db) created by harness.NewTestEnv
		db, err := sql.Open("sqlite", env.DBPath)
		if err != nil {
			t.Fatalf("failed to open sqlite database at %s: %v", env.DBPath, err)
		}
		defer db.Close()

		// Query sqlite_master to verify real database schema and applied tables
		rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
		if err != nil {
			t.Fatalf("failed to query sqlite_master: %v", err)
		}
		defer rows.Close()

		tables := make(map[string]bool)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("failed to scan table name from sqlite_master: %v", err)
			}
			tables[name] = true
		}

		expectedTables := []string{
			"targets",
			"agents",
			"notification_channels",
			"users",
			"audit_log",
			"alert_rules",
			"alert_events",
			"alert_silences",
		}

		for _, tbl := range expectedTables {
			if !tables[tbl] {
				t.Errorf("expected table %q not found in sqlite_master (found tables: %+v)", tbl, tables)
			}
		}
	})

	t.Run("TargetCreationAndListing", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// Create HTTP target
		newTarget := map[string]any{
			"name":        "prod-api-service",
			"type":        "http",
			"target":      "http://127.0.0.1:8080/health",
			"interval_ms": 3000,
			"timeout_ms":  1000,
			"enabled":     true,
			"labels": map[string]string{
				"env":  "production",
				"team": "backend",
			},
		}

		createResp := env.Client.PostJSON(t, "/api/v1/targets", newTarget)
		if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 201 Created or 200 OK, got %d. Body: %s", createResp.StatusCode, createResp.String())
		}

		var created models.Target
		if err := createResp.JSON(&created); err != nil {
			t.Fatalf("failed to parse created target: %v", err)
		}
		if created.ID == "" {
			t.Fatalf("expected non-empty ID assigned to created target")
		}
		if created.Name != "prod-api-service" {
			t.Errorf("expected target name 'prod-api-service', got %s", created.Name)
		}

		// List targets
		listResp := env.Client.Get(t, "/api/v1/targets")
		listResp.AssertOK(t)

		var listResult struct {
			Targets []*models.Target `json:"targets"`
		}
		if err := listResp.JSON(&listResult); err != nil {
			t.Fatalf("failed to parse target list: %v", err)
		}

		found := false
		for _, tg := range listResult.Targets {
			if tg.ID == created.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("created target %s not found in target list (%d targets returned)", created.ID, len(listResult.Targets))
		}
	})

	t.Run("TargetLookupByID", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		newTarget := map[string]any{
			"id":          "target-lookup-test-1",
			"name":        "lookup-target",
			"type":        "tcp",
			"target":      "127.0.0.1:3306",
			"interval_ms": 5000,
			"timeout_ms":  2000,
			"enabled":     true,
		}

		env.Client.PostJSON(t, "/api/v1/targets", newTarget)

		// Lookup by ID
		resp := env.Client.Get(t, "/api/v1/targets/target-lookup-test-1")
		resp.AssertOK(t)

		var lookupResult models.Target
		if err := resp.JSON(&lookupResult); err != nil {
			t.Fatalf("failed to parse lookup result: %v", err)
		}
		if lookupResult.ID != "target-lookup-test-1" || lookupResult.Name != "lookup-target" {
			t.Errorf("unexpected target data returned: %+v", lookupResult)
		}
		if lookupResult.Type != models.TargetTypeTCP {
			t.Errorf("expected target type 'tcp', got %s", lookupResult.Type)
		}
	})

	t.Run("TargetUpdate", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		initTarget := map[string]any{
			"id":          "target-to-update",
			"name":        "original-name",
			"type":        "http",
			"target":      "http://127.0.0.1:8080/old",
			"interval_ms": 10000,
			"timeout_ms":  2000,
			"enabled":     true,
		}
		env.Client.PostJSON(t, "/api/v1/targets", initTarget)

		// Update target properties
		updatePayload := map[string]any{
			"name":        "updated-name",
			"interval_ms": 20000,
			"timeout_ms":  5000,
			"target":      "http://127.0.0.1:8080/new",
			"enabled":     false,
		}
		updateResp := env.Client.PutJSON(t, "/api/v1/targets/target-to-update", updatePayload)
		updateResp.AssertOK(t)

		// Verify updated fields
		getResp := env.Client.Get(t, "/api/v1/targets/target-to-update")
		getResp.AssertOK(t)

		var updated models.Target
		if err := getResp.JSON(&updated); err != nil {
			t.Fatalf("failed to parse updated target: %v", err)
		}
		if updated.Name != "updated-name" {
			t.Errorf("expected name 'updated-name', got %s", updated.Name)
		}
		if updated.IntervalMs != 20000 {
			t.Errorf("expected interval 20000, got %d", updated.IntervalMs)
		}
		if updated.Target != "http://127.0.0.1:8080/new" {
			t.Errorf("expected target URL 'http://127.0.0.1:8080/new', got %s", updated.Target)
		}
		if updated.Enabled != false {
			t.Errorf("expected enabled=false, got true")
		}
	})

	t.Run("TargetDeletion", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		delTarget := map[string]any{
			"id":          "target-to-delete",
			"name":        "ephemeral-target",
			"type":        "http",
			"target":      "http://127.0.0.1:8080/temp",
			"interval_ms": 5000,
			"timeout_ms":  2000,
			"enabled":     true,
		}
		env.Client.PostJSON(t, "/api/v1/targets", delTarget)

		// Delete the target
		delResp := env.Client.Delete(t, "/api/v1/targets/target-to-delete")
		delResp.AssertOK(t)

		// Subsequent GET must return 404
		getResp, err := env.Client.GetRaw("/api/v1/targets/target-to-delete")
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if getResp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found after deletion, got %d", getResp.StatusCode)
		}
	})

	t.Run("AlertRulesAndSilencesCRUD", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// 1. Create Alert Rule
		rulePayload := map[string]any{
			"name":         "HighPacketLoss",
			"expression":   "probe_icmp_packet_loss_percent > 20",
			"for_duration": "1m",
			"severity":     "critical",
			"channels":     []string{"telegram"},
			"enabled":      true,
		}
		ruleResp := env.Client.PostJSON(t, "/api/v1/alerts/rules", rulePayload)
		if ruleResp.StatusCode != http.StatusCreated && ruleResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 201 Created or 200 OK, got %d. Body: %s", ruleResp.StatusCode, ruleResp.String())
		}

		// 2. List Alert Rules
		listRulesResp := env.Client.Get(t, "/api/v1/alerts/rules")
		listRulesResp.AssertOK(t)

		var rulesResult struct {
			Rules []*models.AlertRule `json:"rules"`
		}
		if err := listRulesResp.JSON(&rulesResult); err != nil {
			t.Fatalf("failed to parse alert rules list: %v", err)
		}
		if len(rulesResult.Rules) == 0 {
			t.Fatalf("expected at least 1 alert rule returned")
		}

		// 3. Create Alert Silence
		silencePayload := map[string]any{
			"matchers": `{"target":"prod-api-service"}`,
			"starts_at": "2026-11-23T00:00:00Z",
			"ends_at":   "2026-11-24T00:00:00Z",
			"comment":   "Scheduled maintenance window",
		}
		silResp := env.Client.PostJSON(t, "/api/v1/alerts/silence", silencePayload)
		if silResp.StatusCode != http.StatusCreated && silResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 201 Created or 200 OK for silence, got %d. Body: %s", silResp.StatusCode, silResp.String())
		}
	})
}
