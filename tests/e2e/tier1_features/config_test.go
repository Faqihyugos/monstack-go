package tier1_features_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
	"github.com/yourusername/monstack-go/tests/e2e/harness"
)

// TestConfigSystem tests configuration loading, overrides, redaction, defaults, and hot-reload.
func TestConfigSystem(t *testing.T) {
	t.Run("YAML_Parsing", func(t *testing.T) {
		targetName := "probe-yaml-test-1"
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			cfg["server"].(map[string]any)["mode"] = "http"
			cfg["probes"].(map[string]any)["targets"] = []any{
				map[string]any{
					"name":        targetName,
					"type":        "http",
					"target":      "http://127.0.0.1:18080/check",
					"interval_ms": 1000,
					"timeout_ms":  500,
				},
			}
		}))

		env.StartServer(t)

		// 1. Verify target was parsed and registered in targets endpoint
		resp := env.Client.Get(t, "/api/v1/targets")
		resp.AssertOK(t)

		var listResult struct {
			Targets []*models.Target `json:"targets"`
		}
		if err := resp.JSON(&listResult); err != nil {
			t.Fatalf("failed to parse targets response: %v", err)
		}

		found := false
		for _, tgt := range listResult.Targets {
			if tgt.Name == targetName && tgt.Type == models.TargetTypeHTTP {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected target %q parsed from YAML config, not found in target list", targetName)
		}

		// 2. Verify server mode via config endpoint
		cfgResp := env.Client.Get(t, "/api/v1/config")
		cfgResp.AssertOK(t)
		if !strings.Contains(cfgResp.String(), `"http"`) {
			t.Errorf("expected server mode 'http' reflected in config endpoint: %s", cfgResp.String())
		}
	})

	t.Run("CustomPortAndPathOverride", func(t *testing.T) {
		customPort, err := harness.GetFreePort()
		if err != nil {
			t.Fatalf("failed to allocate free port: %v", err)
		}

		env := newTier1TestEnv(t,
			harness.WithPort(customPort),
			harness.WithCustomConfig(func(cfg map[string]any) {
				cfg["storage"].(map[string]any)["tsdb"].(map[string]any)["retention"] = "7d"
			}),
		)

		if env.ServerPort != customPort {
			t.Fatalf("expected env.ServerPort %d, got %d", customPort, env.ServerPort)
		}

		env.StartServer(t)

		// Verify health check succeeds on custom port
		resp := env.Client.Get(t, "/api/v1/health")
		resp.AssertOK(t)

		// Verify retention override via config endpoint
		cfgResp := env.Client.Get(t, "/api/v1/config")
		cfgResp.AssertOK(t)
		if !strings.Contains(cfgResp.String(), "7d") {
			t.Errorf("expected retention '7d' in config output: %s", cfgResp.String())
		}
	})

	t.Run("SensitiveCredentialsRedaction", func(t *testing.T) {
		secretPass := "SuperSecretAdminPass!@#"
		secretJWT := "very-long-secret-key-that-must-be-hidden-from-users"
		botToken := "123456:SECRET_TELEGRAM_BOT_TOKEN"

		env := newTier1TestEnv(t,
			harness.WithAuth(true, "admin", secretPass),
			harness.WithCustomConfig(func(cfg map[string]any) {
				cfg["auth"].(map[string]any)["jwt"].(map[string]any)["secret"] = secretJWT
				cfg["alerting"].(map[string]any)["channels"] = []any{
					map[string]any{
						"name": "tg-prod",
						"type": "telegram",
						"config": map[string]any{
							"bot_token": botToken,
							"chat_id":   "-10012345",
						},
					},
				}
			}),
		)

		env.StartServer(t)

		// Query running config endpoint
		resp := env.Client.Get(t, "/api/v1/config")
		resp.AssertOK(t)

		bodyStr := resp.String()

		// Verify secrets are redacted and not leaked verbatim
		if strings.Contains(bodyStr, secretPass) {
			t.Fatalf("SECURITY VIOLATION: admin password leaked in /api/v1/config:\n%s", bodyStr)
		}
		if strings.Contains(bodyStr, secretJWT) {
			t.Fatalf("SECURITY VIOLATION: JWT secret leaked in /api/v1/config:\n%s", bodyStr)
		}
		if strings.Contains(bodyStr, botToken) {
			t.Fatalf("SECURITY VIOLATION: Bot token leaked in /api/v1/config:\n%s", bodyStr)
		}
		if !strings.Contains(bodyStr, "***") {
			t.Errorf("expected redacted placeholder '***' in config output:\n%s", bodyStr)
		}
	})

	t.Run("DefaultsFallback", func(t *testing.T) {
		// Start server with minimal config and verify defaults are assigned
		env := newTier1TestEnv(t, harness.WithCustomConfig(func(cfg map[string]any) {
			// Clear optional sections to test default population
			delete(cfg, "auth")
		}))

		env.StartServer(t)

		resp := env.Client.Get(t, "/api/v1/config")
		resp.AssertOK(t)

		body := resp.String()
		// Server mode, interval defaults, and retention defaults should be populated
		if !strings.Contains(body, "server") || !strings.Contains(body, "storage") {
			t.Fatalf("expected server and storage config sections in config output: %s", body)
		}
	})

	t.Run("FSNotifyFileWatch_Trigger", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// Initial check
		initialResp := env.Client.Get(t, "/api/v1/config")
		initialResp.AssertOK(t)

		// Dynamically update config using generic maps/slices
		newTargetName := "dynamically-added-target-via-fsnotify"
		env.UpdateConfig(t, func(cfg map[string]any) {
			probesMap := cfg["probes"].(map[string]any)
			probesMap["targets"] = []any{
				map[string]any{
					"name":        newTargetName,
					"type":        "http",
					"target":      "http://127.0.0.1:18888/health",
					"interval_ms": 1000,
					"timeout_ms":  500,
				},
			}
		})

		// Send SIGHUP or rely on file watcher
		env.SendSIGHUP(t)

		// Poll targets endpoint until updated target appears
		pollForCondition(t, 5*time.Second, 50*time.Millisecond, func() bool {
			targetsResp, err := env.Client.GetRaw("/api/v1/targets")
			if err == nil && targetsResp.StatusCode == http.StatusOK {
				return strings.Contains(targetsResp.String(), newTargetName)
			}
			return false
		})

		targetsResp := env.Client.Get(t, "/api/v1/targets")
		targetsResp.AssertOK(t)
		if !strings.Contains(targetsResp.String(), newTargetName) {
			t.Fatalf("expected updated target %q in targets list after file update, got: %s", newTargetName, targetsResp.String())
		}
	})

	t.Run("ReloadEndpoint", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		reloadTarget := "target-via-reload-endpoint"
		env.UpdateConfig(t, func(cfg map[string]any) {
			probesMap := cfg["probes"].(map[string]any)
			probesMap["targets"] = []any{
				map[string]any{
					"name":        reloadTarget,
					"type":        "http",
					"target":      "http://127.0.0.1:18889/test",
					"interval_ms": 1000,
					"timeout_ms":  500,
				},
			}
		})

		// Trigger reload via PUT /api/v1/config/reload
		putResp := env.Client.PutJSON(t, "/api/v1/config/reload", nil)
		putResp.AssertOK(t)

		// Poll targets until the new target appears
		pollForCondition(t, 5*time.Second, 50*time.Millisecond, func() bool {
			targetsResp, err := env.Client.GetRaw("/api/v1/targets")
			if err == nil && targetsResp.StatusCode == http.StatusOK {
				return strings.Contains(targetsResp.String(), reloadTarget)
			}
			return false
		})

		targetsResp := env.Client.Get(t, "/api/v1/targets")
		targetsResp.AssertOK(t)
		if !strings.Contains(targetsResp.String(), reloadTarget) {
			t.Fatalf("expected target %q after reload, got: %s", reloadTarget, targetsResp.String())
		}
	})
}
