package tier1_features_test

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCLI runs all CLI and lifecycle test cases for MonStack-Go.
func TestCLI(t *testing.T) {
	t.Run("ConfigFlag_Valid", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		resp := env.Client.Get(t, "/api/v1/health")
		resp.AssertOK(t)

		var health map[string]any
		if err := resp.JSON(&health); err != nil {
			t.Fatalf("failed to parse health JSON: %v", err)
		}
		if health["status"] != "up" {
			t.Fatalf("expected status 'up', got %v", health["status"])
		}
	})

	t.Run("ConfigFlag_MissingOrInvalid", func(t *testing.T) {
		bin := resolveBinary(t)
		cmd := exec.Command(bin, "server", "--config", "/nonexistent/invalid_monstack_config.yaml")
		out, err := cmd.CombinedOutput()

		if err == nil {
			t.Fatalf("expected command to fail with non-zero exit code for invalid config, but it succeeded. Output:\n%s", string(out))
		}
		outStr := strings.ToLower(string(out))
		if !strings.Contains(outStr, "error") && !strings.Contains(outStr, "not found") && !strings.Contains(outStr, "no such file") {
			t.Errorf("expected error message in output, got: %s", string(out))
		}
	})

	t.Run("VersionOutput", func(t *testing.T) {
		bin := resolveBinary(t)
		cmd := exec.Command(bin, "version")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version command failed: %v, output: %s", err, string(out))
		}

		outStr := strings.ToLower(string(out))
		if !strings.Contains(outStr, "monstack") || !strings.Contains(outStr, "version") {
			t.Fatalf("expected version output to contain 'monstack' and 'version', got: %s", string(out))
		}
	})

	t.Run("HelpUsage", func(t *testing.T) {
		bin := resolveBinary(t)
		cmd := exec.Command(bin, "--help")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("help command failed: %v, output: %s", err, string(out))
		}

		outStr := string(out)
		if !strings.Contains(outStr, "Usage:") && !strings.Contains(outStr, "usage:") {
			t.Fatalf("expected help output to contain 'Usage:', got: %s", outStr)
		}
		if !strings.Contains(outStr, "server") {
			t.Fatalf("expected help output to mention 'server' command, got: %s", outStr)
		}
	})

	t.Run("GracefulShutdown_SIGTERM", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// Confirm server responds
		resp := env.Client.Get(t, "/api/v1/health")
		resp.AssertOK(t)

		// Gracefully terminate server
		env.StopServer(t)

		// Verify output logs indicate graceful shutdown signal
		logs := env.Logs()
		if !strings.Contains(logs, "graceful shutdown") && !strings.Contains(logs, "shutdown") {
			t.Errorf("expected graceful shutdown log, got:\n%s", logs)
		}

		// Ensure server is no longer accepting requests
		_, err := env.Client.GetRaw("/api/v1/health")
		if err == nil {
			t.Fatalf("expected connection to fail after StopServer, but request succeeded")
		}
	})

	t.Run("SIGHUPHotReload", func(t *testing.T) {
		env := newTier1TestEnv(t)
		env.StartServer(t)

		// Initial check
		resp := env.Client.Get(t, "/api/v1/health")
		resp.AssertOK(t)

		// Send SIGHUP
		env.SendSIGHUP(t)

		// Poll until server continues serving requests cleanly after SIGHUP
		pollForCondition(t, 3*time.Second, 20*time.Millisecond, func() bool {
			r, err := env.Client.GetRaw("/api/v1/health")
			if err == nil && r.StatusCode == http.StatusOK {
				return true
			}
			return false
		})

		respAfter := env.Client.Get(t, "/api/v1/health")
		respAfter.AssertOK(t)

		logs := env.Logs()
		if !strings.Contains(logs, "SIGHUP") && !strings.Contains(logs, "reload") {
			t.Errorf("expected SIGHUP reload log, got:\n%s", logs)
		}
	})
}
