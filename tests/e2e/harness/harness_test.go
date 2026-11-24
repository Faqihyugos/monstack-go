package harness_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/tests/e2e/harness"
)

// TestHelperProcess serves as the mock MonStack server process for TestEnv lifecycle tests.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	listenAddr := os.Getenv("HELPER_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"up","version":"test"}`))
	})

	server := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to listen on %s: %v\n", listenAddr, err)
		os.Exit(1)
	}

	go func() {
		_ = server.Serve(ln)
	}()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	for sig := range sigCh {
		switch sig {
		case syscall.SIGHUP:
			fmt.Fprintln(os.Stdout, "received SIGHUP hot-reload")
		case syscall.SIGTERM, syscall.SIGINT:
			fmt.Fprintln(os.Stdout, "received graceful shutdown signal")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = server.Shutdown(ctx)
			cancel()
			os.Exit(0)
		}
	}
}

// TestMockHTTPServer verifies HTTP/HTTPS mock server routes, latency, certs, and request logging.
func TestMockHTTPServer(t *testing.T) {
	t.Run("DefaultRoute", func(t *testing.T) {
		srv := harness.NewMockHTTPServer()
		defer srv.Close()

		resp, err := http.Get(srv.URL())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"status":"ok"}` {
			t.Fatalf("expected {\"status\":\"ok\"}, got %s", string(body))
		}

		if srv.RequestCount() != 1 {
			t.Fatalf("expected 1 request, got %d", srv.RequestCount())
		}
		req := srv.LastRequest()
		if req.Method != http.MethodGet || req.Path != "/" {
			t.Fatalf("unexpected request record: %+v", req)
		}
	})

	t.Run("CustomStatusCodesAndRoutes", func(t *testing.T) {
		srv := harness.NewMockHTTPServer()
		defer srv.Close()

		srv.AddStatusRoute("/bad-request", http.StatusBadRequest, "Invalid parameter")
		srv.AddStatusRoute("/not-found", http.StatusNotFound)
		srv.AddStatusRoute("/error", http.StatusInternalServerError, "DB error")
		srv.AddStatusRoute("/gateway-timeout", http.StatusGatewayTimeout)

		codes := []struct {
			path     string
			expected int
		}{
			{"/bad-request", http.StatusBadRequest},
			{"/not-found", http.StatusNotFound},
			{"/error", http.StatusInternalServerError},
			{"/gateway-timeout", http.StatusGatewayTimeout},
		}

		for _, tc := range codes {
			resp, err := http.Get(srv.URL() + tc.path)
			if err != nil {
				t.Fatalf("GET %s failed: %v", tc.path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.expected {
				t.Errorf("path %s: expected %d, got %d", tc.path, tc.expected, resp.StatusCode)
			}
		}

		if srv.RequestCount() != len(codes) {
			t.Fatalf("expected %d requests, got %d", len(codes), srv.RequestCount())
		}
	})

	t.Run("ContentMatchRoute", func(t *testing.T) {
		srv := harness.NewMockHTTPServer()
		defer srv.Close()

		srv.AddContentMatchRoute("/api/v1/ready", "READY_FOR_TRAFFIC", "text/plain")

		resp, err := http.Get(srv.URL() + "/api/v1/ready")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "READY_FOR_TRAFFIC" {
			t.Fatalf("expected 'READY_FOR_TRAFFIC', got %s", string(body))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
			t.Fatalf("unexpected Content-Type: %s", ct)
		}
	})

	t.Run("SimulatedDelay", func(t *testing.T) {
		srv := harness.NewMockHTTPServer()
		defer srv.Close()

		delay := 150 * time.Millisecond
		srv.SetDelay(delay)

		start := time.Now()
		resp, err := http.Get(srv.URL() + "/delayed")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		elapsed := time.Since(start)

		if elapsed < delay {
			t.Fatalf("expected response time >= %s, got %s", delay, elapsed)
		}
	})

	t.Run("RedirectChains", func(t *testing.T) {
		srv := harness.NewMockHTTPServer()
		defer srv.Close()

		srv.AddRedirectChain("/start", 3, "/final", "FINAL_PAYLOAD")

		// Standard client follows redirects automatically
		client := &http.Client{}
		resp, err := client.Get(srv.URL() + "/start")
		if err != nil {
			t.Fatalf("redirect chain failed: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "FINAL_PAYLOAD" {
			t.Fatalf("expected 'FINAL_PAYLOAD', got %s", string(body))
		}
	})

	t.Run("CustomTLSCertValidAndExpiring", func(t *testing.T) {
		// 1. Valid certificate
		httpsSrv, err := harness.NewMockHTTPSServer(harness.TLSCertOptions{
			CommonName: "127.0.0.1",
			ValidFor:   30 * 24 * time.Hour,
		})
		if err != nil {
			t.Fatalf("failed to create HTTPS mock: %v", err)
		}
		defer httpsSrv.Close()

		cert := httpsSrv.TLSCertificate()
		if cert == nil {
			t.Fatalf("expected parsed TLS certificate, got nil")
		}
		if time.Until(cert.NotAfter) < 29*24*time.Hour {
			t.Fatalf("expected valid cert with ~30 days left, got NotAfter=%v", cert.NotAfter)
		}

		client := httpsSrv.Client()
		resp, err := client.Get(httpsSrv.URL() + "/secure")
		if err != nil {
			t.Fatalf("HTTPS GET failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		// 2. Expired certificate
		expiredSrv, err := harness.NewMockHTTPSServer(harness.TLSCertOptions{
			CommonName: "127.0.0.1",
			NotBefore:  time.Now().Add(-48 * time.Hour),
			NotAfter:   time.Now().Add(-24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("failed to create expired HTTPS mock: %v", err)
		}
		defer expiredSrv.Close()

		expCert := expiredSrv.TLSCertificate()
		if time.Now().Before(expCert.NotAfter) {
			t.Fatalf("expected certificate to be expired, NotAfter=%v", expCert.NotAfter)
		}
	})

	t.Run("ConcurrentRequestsAndClear", func(t *testing.T) {
		srv := harness.NewMockHTTPServer()
		defer srv.Close()

		var wg sync.WaitGroup
		numRequests := 25
		for i := 0; i < numRequests; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				u := fmt.Sprintf("%s/test?id=%d", srv.URL(), idx)
				resp, err := http.Get(u)
				if err == nil {
					resp.Body.Close()
				}
			}(i)
		}
		wg.Wait()

		if srv.RequestCount() != numRequests {
			t.Fatalf("expected %d requests, got %d", numRequests, srv.RequestCount())
		}

		srv.ClearRequests()
		if srv.RequestCount() != 0 {
			t.Fatalf("expected 0 requests after ClearRequests, got %d", srv.RequestCount())
		}
	})
}

// TestMockTCPServer verifies TCP connections, greetings, echo, and expect matching.
func TestMockTCPServer(t *testing.T) {
	t.Run("BannerTransmission", func(t *testing.T) {
		srv, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to create mock TCP server: %v", err)
		}
		defer srv.Close()

		srv.SetBanner("SSH-2.0-OpenSSH_9.0\r\n")

		conn, err := net.DialTimeout("tcp", srv.Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("failed to dial mock TCP server: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 128)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("failed to read banner: %v", err)
		}
		banner := string(buf[:n])
		if banner != "SSH-2.0-OpenSSH_9.0\r\n" {
			t.Fatalf("expected OpenSSH banner, got %q", banner)
		}

		if srv.ConnectionCount() != 1 {
			t.Fatalf("expected 1 connection, got %d", srv.ConnectionCount())
		}
	})

	t.Run("EchoMode", func(t *testing.T) {
		srv, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to create mock TCP server: %v", err)
		}
		defer srv.Close()

		srv.SetEchoMode(true)

		conn, err := net.DialTimeout("tcp", srv.Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("failed to dial mock TCP server: %v", err)
		}
		defer conn.Close()

		msg := "PING-DATA-1234\n"
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatalf("failed to write: %v", err)
		}

		buf := make([]byte, 128)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("failed to read echo: %v", err)
		}
		if string(buf[:n]) != msg {
			t.Fatalf("expected echo %q, got %q", msg, string(buf[:n]))
		}
	})

	t.Run("ExpectMatch", func(t *testing.T) {
		srv, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to create mock TCP server: %v", err)
		}
		defer srv.Close()

		srv.SetExpectMatch("HELO", "250 smtp.monstack.local Hello\r\n")

		conn, err := net.DialTimeout("tcp", srv.Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("failed to dial: %v", err)
		}
		defer conn.Close()

		if _, err := conn.Write([]byte("HELO client.local\r\n")); err != nil {
			t.Fatalf("failed to write HELO: %v", err)
		}

		buf := make([]byte, 128)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("failed to read expect response: %v", err)
		}
		if string(buf[:n]) != "250 smtp.monstack.local Hello\r\n" {
			t.Fatalf("unexpected expect match response: %q", string(buf[:n]))
		}
	})

	t.Run("ImmediateClose", func(t *testing.T) {
		srv, err := harness.NewMockTCPServer()
		if err != nil {
			t.Fatalf("failed to create mock TCP server: %v", err)
		}
		defer srv.Close()

		srv.SetImmediateClose(true)

		conn, err := net.DialTimeout("tcp", srv.Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("failed to dial: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		if err != io.EOF && n > 0 {
			t.Fatalf("expected immediate EOF, got n=%d, err=%v", n, err)
		}
	})
}

// TestMockTelegramServer verifies Telegram Bot API message interception and assertions.
func TestMockTelegramServer(t *testing.T) {
	t.Run("JSONSendMessageAndResponse", func(t *testing.T) {
		tg := harness.NewMockTelegramServer()
		defer tg.Close()

		targetURL := fmt.Sprintf("%s/bot123456:ABC-DEF/sendMessage", tg.URL())
		jsonPayload := `{"chat_id": 987654, "text": "Critical alert: Target DOWN", "parse_mode": "HTML"}`

		resp, err := http.Post(targetURL, "application/json", strings.NewReader(jsonPayload))
		if err != nil {
			t.Fatalf("POST to Telegram mock failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"ok":true`) {
			t.Fatalf("expected ok:true in response, got %s", string(body))
		}

		msg, err := tg.WaitForMessage(1 * time.Second)
		if err != nil {
			t.Fatalf("failed to retrieve telegram message: %v", err)
		}
		if msg.Token != "123456:ABC-DEF" {
			t.Errorf("expected token '123456:ABC-DEF', got %q", msg.Token)
		}
		if msg.ChatID != "987654" {
			t.Errorf("expected chat_id '987654', got %q", msg.ChatID)
		}
		if msg.Text != "Critical alert: Target DOWN" {
			t.Errorf("expected alert text, got %q", msg.Text)
		}
	})

	t.Run("FormSendMessageAndReset", func(t *testing.T) {
		tg := harness.NewMockTelegramServer()
		defer tg.Close()

		targetURL := fmt.Sprintf("%s/botmytoken/sendMessage", tg.URL())
		formData := url.Values{
			"chat_id":    {"-1001234567890"},
			"text":       {"Warning: High Latency"},
			"parse_mode": {"Markdown"},
		}

		resp, err := http.PostForm(targetURL, formData)
		if err != nil {
			t.Fatalf("POST form failed: %v", err)
		}
		resp.Body.Close()

		if tg.MessageCount() != 1 {
			t.Fatalf("expected 1 message, got %d", tg.MessageCount())
		}
		last := tg.LastMessage()
		if last.ChatID != "-1001234567890" || last.Text != "Warning: High Latency" {
			t.Fatalf("unexpected message: %+v", last)
		}

		tg.Reset()
		if tg.MessageCount() != 0 {
			t.Fatalf("expected 0 messages after Reset, got %d", tg.MessageCount())
		}
	})

	t.Run("SimulatedFailure", func(t *testing.T) {
		tg := harness.NewMockTelegramServer()
		defer tg.Close()

		tg.SetFailure(http.StatusTooManyRequests)

		targetURL := fmt.Sprintf("%s/bottoken/sendMessage", tg.URL())
		resp, err := http.Post(targetURL, "application/json", strings.NewReader(`{"text":"test"}`))
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", resp.StatusCode)
		}
	})
}

// TestTestEnv verifies sandbox isolation, configuration generation, client, and process lifecycle.
func TestTestEnv(t *testing.T) {
	t.Run("SandboxPathsAndPortAllocation", func(t *testing.T) {
		env := harness.NewTestEnv(t)

		if env.RootDir == "" {
			t.Fatalf("expected non-empty RootDir")
		}
		if env.ServerPort <= 0 {
			t.Fatalf("expected valid allocated server port, got %d", env.ServerPort)
		}
		if env.GRPCPort <= 0 {
			t.Fatalf("expected valid allocated grpc port, got %d", env.GRPCPort)
		}
		if env.ServerPort == env.GRPCPort {
			t.Fatalf("expected server port and grpc port to differ: %d vs %d", env.ServerPort, env.GRPCPort)
		}

		// Verify files exist in sandbox
		if _, err := os.Stat(env.ConfigPath); err != nil {
			t.Fatalf("config file missing: %v", err)
		}
		if _, err := os.Stat(env.TSDBPath); err != nil {
			t.Fatalf("tsdb dir missing: %v", err)
		}
	})

	t.Run("ConfigMutation", func(t *testing.T) {
		env := harness.NewTestEnv(t)

		env.UpdateConfig(t, func(cfg map[string]any) {
			serverMap := cfg["server"].(map[string]any)
			serverMap["mode"] = "https"
		})

		data, err := os.ReadFile(env.ConfigPath)
		if err != nil {
			t.Fatalf("failed to read updated config: %v", err)
		}
		if !strings.Contains(string(data), `mode: "https"`) && !strings.Contains(string(data), `mode: https`) {
			t.Fatalf("updated mode not found in config: %s", string(data))
		}
	})

	t.Run("ClientRESTHelpers", func(t *testing.T) {
		// Mock server to test Client Get, PostJSON, PutJSON, Delete
		mockSrv := harness.NewMockHTTPServer()
		defer mockSrv.Close()

		mockSrv.AddRoute("/api/v1/items", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"items":["alpha","beta"]}`))
			case http.MethodPost:
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"item-123"}`))
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		})

		client := harness.NewClient(mockSrv.URL())
		client.SetBearerToken("secret-token")

		// 1. GET
		getResp := client.Get(t, "/api/v1/items")
		getResp.AssertOK(t)
		var listResult map[string][]string
		if err := getResp.JSON(&listResult); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}
		if len(listResult["items"]) != 2 {
			t.Fatalf("expected 2 items, got %v", listResult["items"])
		}

		// 2. POST
		postResp := client.PostJSON(t, "/api/v1/items", map[string]string{"name": "gamma"})
		postResp.AssertStatus(t, http.StatusCreated)
		var createResult map[string]string
		if err := postResp.JSON(&createResult); err != nil {
			t.Fatalf("failed to parse create result: %v", err)
		}
		if createResult["id"] != "item-123" {
			t.Fatalf("expected id 'item-123', got %s", createResult["id"])
		}
	})

	t.Run("ServerProcessLifecycleMock", func(t *testing.T) {
		// Verify StartServer, polling health check, SendSIGHUP, and StopServer with mock process
		port, err := harness.GetFreePort()
		if err != nil {
			t.Fatalf("failed to get free port: %v", err)
		}

		listenAddr := fmt.Sprintf("127.0.0.1:%d", port)

		env := harness.NewTestEnv(t,
			harness.WithPort(port),
			harness.WithCommandFunc(func(configPath string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
				cmd.Env = append(os.Environ(),
					"GO_WANT_HELPER_PROCESS=1",
					fmt.Sprintf("HELPER_LISTEN_ADDR=%s", listenAddr),
					fmt.Sprintf("CONFIG_PATH=%s", configPath),
				)
				return cmd
			}),
		)

		env.StartServer(t)

		// Verify health check response
		resp := env.Client.Get(t, "/api/v1/health")
		resp.AssertOK(t)

		// Send SIGHUP
		env.SendSIGHUP(t)

		// Stop server gracefully
		env.StopServer(t)

		// Verify process output captured in logs
		logs := env.Logs()
		if !strings.Contains(logs, "received SIGHUP hot-reload") {
			t.Errorf("expected SIGHUP log in process logs, got: %s", logs)
		}
		if !strings.Contains(logs, "received graceful shutdown signal") {
			t.Errorf("expected shutdown signal log in process logs, got: %s", logs)
		}
	})
}
