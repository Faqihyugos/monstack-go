package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// SafeBuffer provides a thread-safe bytes buffer for capturing process logs.
type SafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends bytes to buffer.
func (b *SafeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns the buffered content as string.
func (b *SafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Bytes returns a copy of the buffer content.
func (b *SafeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := make([]byte, b.buf.Len())
	copy(res, b.buf.Bytes())
	return res
}

// Reset clears the buffer.
func (b *SafeBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// APIResponse encapsulates the HTTP response returned by Client.
type APIResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// JSON unmarshals the response body into dest.
func (r *APIResponse) JSON(dest any) error {
	return json.Unmarshal(r.Body, dest)
}

// String returns body as string.
func (r *APIResponse) String() string {
	return string(r.Body)
}

// AssertStatus fails the test if response status code does not match expected.
func (r *APIResponse) AssertStatus(t testing.TB, expected int) {
	t.Helper()
	if r.StatusCode != expected {
		t.Fatalf("expected HTTP status %d, got %d. Body: %s", expected, r.StatusCode, r.String())
	}
}

// AssertOK fails the test if response status code is not 200 OK.
func (r *APIResponse) AssertOK(t testing.TB) {
	t.Helper()
	r.AssertStatus(t, http.StatusOK)
}

// Client is an HTTP REST client configured to interact with the MonStack server.
type Client struct {
	baseURL    string
	httpClient *http.Client
	headers    map[string]string
	mu         sync.RWMutex
}

// NewClient creates a new REST client for the given baseURL (e.g. "http://127.0.0.1:8080").
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		headers: make(map[string]string),
	}
}

// SetHeader sets a persistent header on all outgoing requests.
func (c *Client) SetHeader(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers[key] = value
}

// SetBearerToken sets the Authorization: Bearer <token> header.
func (c *Client) SetBearerToken(token string) {
	c.SetHeader("Authorization", "Bearer "+token)
}

// SetBasicAuth sets standard HTTP Basic Authentication header.
func (c *Client) SetBasicAuth(username, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	req, _ := http.NewRequest(http.MethodGet, "http://localhost", nil)
	req.SetBasicAuth(username, password)
	c.headers["Authorization"] = req.Header.Get("Authorization")
}

// ClearAuth removes authorization headers.
func (c *Client) ClearAuth() {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.headers, "Authorization")
}

// Do executes an HTTP request and returns an APIResponse.
func (c *Client) Do(req *http.Request) (*APIResponse, error) {
	c.mu.RLock()
	for k, v := range c.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	c.mu.RUnlock()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return &APIResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       bodyBytes,
	}, nil
}

// Get executes a GET request against the given path.
func (c *Client) Get(t testing.TB, path string) *APIResponse {
	t.Helper()
	resp, err := c.GetRaw(path)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	return resp
}

// GetRaw executes a GET request and returns an APIResponse and error without failing test.
func (c *Client) GetRaw(path string) (*APIResponse, error) {
	u := c.baseURL + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// PostJSON executes a POST request with JSON payload.
func (c *Client) PostJSON(t testing.TB, path string, body any) *APIResponse {
	t.Helper()
	resp, err := c.PostJSONRaw(path, body)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	return resp
}

// PostJSONRaw executes a POST request with JSON payload and returns error without failing test.
func (c *Client) PostJSONRaw(path string, body any) (*APIResponse, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	u := c.baseURL + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequest(http.MethodPost, u, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

// PutJSON executes a PUT request with JSON payload.
func (c *Client) PutJSON(t testing.TB, path string, body any) *APIResponse {
	t.Helper()
	resp, err := c.PutJSONRaw(path, body)
	if err != nil {
		t.Fatalf("PUT %s failed: %v", path, err)
	}
	return resp
}

// PutJSONRaw executes a PUT request with JSON payload and returns error without failing test.
func (c *Client) PutJSONRaw(path string, body any) (*APIResponse, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	u := c.baseURL + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequest(http.MethodPut, u, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

// Delete executes a DELETE request against the given path.
func (c *Client) Delete(t testing.TB, path string) *APIResponse {
	t.Helper()
	resp, err := c.DeleteRaw(path)
	if err != nil {
		t.Fatalf("DELETE %s failed: %v", path, err)
	}
	return resp
}

// DeleteRaw executes a DELETE request and returns error without failing test.
func (c *Client) DeleteRaw(path string) (*APIResponse, error) {
	u := c.baseURL + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// EnvConfig configures TestEnv creation options.
type EnvConfig struct {
	ServerPort    int
	GRPCPort      int
	AuthEnabled   bool
	AdminUser     string
	AdminPassword string
	LogLevel      string
	LogFormat     string
	KeepTempDir   bool
	BinaryPath    string
	CommandFunc   func(configPath string) *exec.Cmd
	CustomConfig  func(cfg map[string]any)
}

// EnvOption configures a TestEnv instance.
type EnvOption func(*EnvConfig)

// WithPort sets explicit HTTP server port.
func WithPort(port int) EnvOption {
	return func(c *EnvConfig) {
		c.ServerPort = port
	}
}

// WithGRPCPort sets explicit gRPC port.
func WithGRPCPort(port int) EnvOption {
	return func(c *EnvConfig) {
		c.GRPCPort = port
	}
}

// WithAuth configures authentication in monstack.yaml.
func WithAuth(enabled bool, user, pass string) EnvOption {
	return func(c *EnvConfig) {
		c.AuthEnabled = enabled
		c.AdminUser = user
		c.AdminPassword = pass
	}
}

// WithLogLevel sets logging level.
func WithLogLevel(level string) EnvOption {
	return func(c *EnvConfig) {
		c.LogLevel = level
	}
}

// WithKeepTempDir preserves the sandbox temporary directory after test completion.
func WithKeepTempDir() EnvOption {
	return func(c *EnvConfig) {
		c.KeepTempDir = true
	}
}

// WithBinaryPath specifies the binary executable path to run.
func WithBinaryPath(p string) EnvOption {
	return func(c *EnvConfig) {
		c.BinaryPath = p
	}
}

// WithCommandFunc overrides the process execution generator (for testing process lifecycle).
func WithCommandFunc(f func(configPath string) *exec.Cmd) EnvOption {
	return func(c *EnvConfig) {
		c.CommandFunc = f
	}
}

// WithCustomConfig allows mutating the generated initial YAML config map.
func WithCustomConfig(f func(cfg map[string]any)) EnvOption {
	return func(c *EnvConfig) {
		c.CustomConfig = f
	}
}

// TestEnv manages an isolated MonStack sandbox environment and server process.
type TestEnv struct {
	RootDir     string
	DBPath      string
	TSDBPath    string
	ConfigPath  string
	ServerPort  int
	GRPCPort    int
	ConfigMap   map[string]any
	Client      *Client
	BinaryPath  string
	cmd         *exec.Cmd
	cmdFunc     func(configPath string) *exec.Cmd
	logBuffer   *SafeBuffer
	keepTempDir bool
	mu          sync.RWMutex
	running     bool
}

// GetFreePort finds an available TCP port on the loopback interface.
func GetFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to find free TCP port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// NewTestEnv creates and initializes an isolated test sandbox environment for a test.
func NewTestEnv(t testing.TB, opts ...EnvOption) *TestEnv {
	t.Helper()

	cfg := &EnvConfig{
		AuthEnabled:   false,
		AdminUser:     "admin",
		AdminPassword: "testpassword123",
		LogLevel:      "debug",
		LogFormat:     "json",
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// 1. Allocate ports if not explicitly specified
	if cfg.ServerPort == 0 {
		p, err := GetFreePort()
		if err != nil {
			t.Fatalf("failed to allocate server port: %v", err)
		}
		cfg.ServerPort = p
	}

	if cfg.GRPCPort == 0 {
		p, err := GetFreePort()
		if err != nil {
			t.Fatalf("failed to allocate grpc port: %v", err)
		}
		cfg.GRPCPort = p
	}

	// 2. Create isolated temp directory
	tempDir, err := os.MkdirTemp("", "monstack-e2e-*")
	if err != nil {
		t.Fatalf("failed to create sandbox directory: %v", err)
	}

	dbPath := filepath.Join(tempDir, "meta.db")
	tsdbPath := filepath.Join(tempDir, "tsdb_data")
	configPath := filepath.Join(tempDir, "monstack.yaml")

	if err := os.MkdirAll(tsdbPath, 0755); err != nil {
		t.Fatalf("failed to create tsdb data directory: %v", err)
	}

	// 3. Assemble initial configuration map
	configMap := map[string]any{
		"server": map[string]any{
			"listen":        fmt.Sprintf("127.0.0.1:%d", cfg.ServerPort),
			"mode":          "http",
			"read_timeout":  "15s",
			"write_timeout": "15s",
		},
		"auth": map[string]any{
			"enabled":        cfg.AuthEnabled,
			"admin_user":     cfg.AdminUser,
			"admin_password": cfg.AdminPassword,
			"jwt": map[string]any{
				"secret": "e2e-test-super-secret-jwt-key-minimum-32-chars",
				"expiry": "24h",
			},
		},
		"storage": map[string]any{
			"tsdb": map[string]any{
				"path":      tsdbPath,
				"retention": "24h",
				"max_disk":  "1GB",
			},
			"metadata": map[string]any{
				"path": dbPath,
			},
			"cache": map[string]any{
				"enabled":    true,
				"max_memory": "32MB",
			},
		},
		"grpc": map[string]any{
			"listen":            fmt.Sprintf("127.0.0.1:%d", cfg.GRPCPort),
			"tls":               false,
			"max_recv_msg_size": "16MB",
		},
		"probes": map[string]any{
			"defaults": map[string]any{
				"interval": "5s",
				"timeout":  "3s",
				"retries":  1,
			},
			"targets": []any{},
		},
		"alerting": map[string]any{
			"evaluation_interval": "2s",
			"rules":               []any{},
			"channels":            []any{},
			"routing": map[string]any{
				"default_channel": "",
			},
		},
		"discovery": []any{},
		"logging": map[string]any{
			"level":  cfg.LogLevel,
			"format": cfg.LogFormat,
			"output": "stdout",
		},
	}

	if cfg.CustomConfig != nil {
		cfg.CustomConfig(configMap)
	}

	// 4. Serialize initial config file
	yamlData, err := MarshalSimpleYAML(configMap)
	if err != nil {
		t.Fatalf("failed to marshal initial YAML config: %v", err)
	}

	if err := os.WriteFile(configPath, yamlData, 0644); err != nil {
		t.Fatalf("failed to write sandbox config file: %v", err)
	}

	serverURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.ServerPort)
	client := NewClient(serverURL)
	if cfg.AuthEnabled {
		client.SetBasicAuth(cfg.AdminUser, cfg.AdminPassword)
	}

	env := &TestEnv{
		RootDir:     tempDir,
		DBPath:      dbPath,
		TSDBPath:    tsdbPath,
		ConfigPath:  configPath,
		ServerPort:  cfg.ServerPort,
		GRPCPort:    cfg.GRPCPort,
		ConfigMap:   configMap,
		Client:      client,
		BinaryPath:  cfg.BinaryPath,
		cmdFunc:     cfg.CommandFunc,
		logBuffer:   &SafeBuffer{},
		keepTempDir: cfg.KeepTempDir,
	}

	// 5. Automatic cleanup registration
	t.Cleanup(func() {
		env.StopServer(t)
		if !env.keepTempDir {
			_ = os.RemoveAll(tempDir)
		}
	})

	return env
}

// ServerURL returns the base URL of the MonStack server.
func (e *TestEnv) ServerURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", e.ServerPort)
}

// Logs returns process standard output and standard error logs.
func (e *TestEnv) Logs() string {
	return e.logBuffer.String()
}

// StartServer starts the MonStack server process and polls the health endpoint until healthy.
func (e *TestEnv) StartServer(t testing.TB) {
	t.Helper()
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}

	// Resolve command to execute
	var cmd *exec.Cmd
	if e.cmdFunc != nil {
		cmd = e.cmdFunc(e.ConfigPath)
	} else {
		binPath := e.resolveBinaryPath(t)
		cmd = exec.Command(binPath, "server", "--config", e.ConfigPath)
	}

	// Capture stdout and stderr
	cmd.Stdout = io.MultiWriter(e.logBuffer, os.Stdout)
	cmd.Stderr = io.MultiWriter(e.logBuffer, os.Stderr)

	// Set process group for clean signal handling on POSIX
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		t.Fatalf("failed to start server process: %v\nLogs:\n%s", err, e.logBuffer.String())
	}

	e.cmd = cmd
	e.running = true
	e.mu.Unlock()

	// Poll health endpoint until healthy or timeout
	healthURL := fmt.Sprintf("%s/api/v1/health", e.ServerURL())
	pollCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	httpClient := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			e.StopServer(t)
			t.Fatalf("server failed to become healthy at %s within 15s.\nLogs:\n%s", healthURL, e.logBuffer.String())
		case <-ticker.C:
			// Check if process terminated prematurely
			e.mu.RLock()
			running := e.running
			e.mu.RUnlock()
			if !running {
				t.Fatalf("server process exited unexpectedly during startup.\nLogs:\n%s", e.logBuffer.String())
			}

			req, _ := http.NewRequestWithContext(pollCtx, http.MethodGet, healthURL, nil)
			resp, err := httpClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					// Server is ready!
					return
				}
			}
		}
	}
}

// StopServer gracefully terminates the server process using SIGTERM or SIGINT.
func (e *TestEnv) StopServer(t testing.TB) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running || e.cmd == nil || e.cmd.Process == nil {
		return
	}

	// Send SIGTERM
	_ = e.cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		done <- e.cmd.Wait()
	}()

	select {
	case <-done:
		// Exited
	case <-time.After(5 * time.Second):
		// Force kill if not exited after 5s
		_ = e.cmd.Process.Kill()
		<-done
	}

	e.running = false
	e.cmd = nil
}

// SendSIGHUP sends a SIGHUP signal to the running server to trigger hot-reload.
func (e *TestEnv) SendSIGHUP(t testing.TB) {
	t.Helper()
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.running || e.cmd == nil || e.cmd.Process == nil {
		t.Fatalf("cannot send SIGHUP: server process is not running")
	}

	if err := e.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("failed to send SIGHUP to pid %d: %v", e.cmd.Process.Pid, err)
	}
}

// UpdateConfig applies a mutator function to ConfigMap and rewrites monstack.yaml.
func (e *TestEnv) UpdateConfig(t testing.TB, mutator func(cfg map[string]any)) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()

	mutator(e.ConfigMap)

	yamlData, err := MarshalSimpleYAML(e.ConfigMap)
	if err != nil {
		t.Fatalf("failed to marshal updated config to YAML: %v", err)
	}

	if err := os.WriteFile(e.ConfigPath, yamlData, 0644); err != nil {
		t.Fatalf("failed to write updated config file %s: %v", e.ConfigPath, err)
	}

	// Brief pause to allow fsnotify event propagation
	time.Sleep(150 * time.Millisecond)
}

// FindBinary locates an existing monstack binary on disk without building.
func FindBinary() string {
	if p := os.Getenv("MONSTACK_BINARY"); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	candidates := []string{
		"bin/monstack",
		"../bin/monstack",
		"../../bin/monstack",
		"../../../bin/monstack",
	}

	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(cand)
			return abs
		}
	}
	return ""
}

// HasBinary reports whether the monstack binary is currently available.
func HasBinary() bool {
	return FindBinary() != ""
}

// ResolveBinaryPath locates the monstack binary or compiles it if cmd/monstack/main.go exists.
// If cmd/monstack/main.go does not exist or has no Go files, it returns an error rather than failing with build errors.
func ResolveBinaryPath() (string, error) {
	if bin := FindBinary(); bin != "" {
		return bin, nil
	}

	cmdDirs := []string{
		"cmd/monstack",
		"../cmd/monstack",
		"../../cmd/monstack",
	}

	for _, cmdDir := range cmdDirs {
		mainGo := filepath.Join(cmdDir, "main.go")
		if fi, err := os.Stat(mainGo); err == nil && !fi.IsDir() {
			binDir := filepath.Join(filepath.Dir(cmdDir), "..", "bin")
			_ = os.MkdirAll(binDir, 0755)
			targetBin := filepath.Join(binDir, "monstack")

			buildCmd := exec.Command("go", "build", "-o", targetBin, "./"+cmdDir)
			out, err := buildCmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("failed to compile monstack binary: %v\nOutput:\n%s", err, string(out))
			}
			abs, _ := filepath.Abs(targetBin)
			return abs, nil
		}
	}

	return "", fmt.Errorf("monstack binary not found at bin/monstack and cmd/monstack/main.go does not exist")
}

// RequireBinary ensures the monstack binary is available.
// If not built, it checks os.Getenv("MONSTACK_E2E_STRICT") == "1" (or REQUIRE_MONSTACK_BINARY=1):
// if strict, t.Fatalf("monstack binary not found at %s", path);
// otherwise t.Skip("monstack binary not yet built; skipping E2E test until M6").
func RequireBinary(t testing.TB) string {
	t.Helper()
	bin, err := ResolveBinaryPath()
	if err == nil && bin != "" {
		return bin
	}

	path := "bin/monstack"
	if os.Getenv("MONSTACK_E2E_STRICT") == "1" || os.Getenv("REQUIRE_MONSTACK_BINARY") == "1" {
		t.Fatalf("monstack binary not found at %s", path)
	}
	t.Skip("monstack binary not yet built; skipping E2E test until M6")
	return ""
}

// resolveBinaryPath finds the monstack binary or skips/fails via RequireBinary.
func (e *TestEnv) resolveBinaryPath(t testing.TB) string {
	t.Helper()
	if e.BinaryPath != "" {
		if fi, err := os.Stat(e.BinaryPath); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(e.BinaryPath)
			return abs
		}
	}
	return RequireBinary(t)
}

// PollUntil polls cond every interval until it returns true or timeout expires.
func PollUntil(timeout, interval time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(interval)
	}
	return cond()
}

// MarshalSimpleYAML converts a generic Go structure (maps, slices, primitives) into clean YAML.
func MarshalSimpleYAML(val any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeYAMLValue(&buf, val, 0, false); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeYAMLValue(buf *bytes.Buffer, val any, indent int, isItem bool) error {
	spaces := strings.Repeat("  ", indent)

	switch v := val.(type) {
	case map[string]any:
		if len(v) == 0 {
			buf.WriteString("{}\n")
			return nil
		}
		if isItem {
			// Item prefix already written
		} else if indent > 0 {
			buf.WriteString("\n")
		}

		// Sort keys for deterministic output
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for i, k := range keys {
			itemIndent := spaces
			if isItem && i == 0 {
				itemIndent = ""
			}
			subVal := v[k]
			switch sv := subVal.(type) {
			case map[string]any:
				if len(sv) == 0 {
					buf.WriteString(fmt.Sprintf("%s%s: {}\n", itemIndent, k))
				} else {
					buf.WriteString(fmt.Sprintf("%s%s:", itemIndent, k))
					if err := writeYAMLValue(buf, sv, indent+1, false); err != nil {
						return err
					}
				}
			case []any:
				if len(sv) == 0 {
					buf.WriteString(fmt.Sprintf("%s%s: []\n", itemIndent, k))
				} else {
					buf.WriteString(fmt.Sprintf("%s%s:\n", itemIndent, k))
					if err := writeYAMLValue(buf, sv, indent+1, false); err != nil {
						return err
					}
				}
			default:
				buf.WriteString(fmt.Sprintf("%s%s: ", itemIndent, k))
				if err := writeYAMLValue(buf, subVal, indent+1, false); err != nil {
					return err
				}
			}
		}
		return nil

	case []any:
		if len(v) == 0 {
			buf.WriteString("[]\n")
			return nil
		}
		for _, item := range v {
			buf.WriteString(fmt.Sprintf("%s- ", spaces))
			switch it := item.(type) {
			case map[string]any:
				if err := writeYAMLValue(buf, it, indent+1, true); err != nil {
					return err
				}
			default:
				if err := writeYAMLValue(buf, item, indent+1, false); err != nil {
					return err
				}
			}
		}
		return nil

	case []string:
		if len(v) == 0 {
			buf.WriteString("[]\n")
			return nil
		}
		for _, item := range v {
			buf.WriteString(fmt.Sprintf("%s- %s\n", spaces, formatYAMLString(item)))
		}
		return nil

	case string:
		buf.WriteString(formatYAMLString(v) + "\n")
		return nil

	case bool:
		buf.WriteString(strconv.FormatBool(v) + "\n")
		return nil

	case int:
		buf.WriteString(strconv.Itoa(v) + "\n")
		return nil

	case int64:
		buf.WriteString(strconv.FormatInt(v, 10) + "\n")
		return nil

	case float64:
		buf.WriteString(strconv.FormatFloat(v, 'f', -1, 64) + "\n")
		return nil

	case nil:
		buf.WriteString("null\n")
		return nil

	default:
		buf.WriteString(fmt.Sprintf("%v\n", v))
		return nil
	}
}

// formatYAMLString quotes strings containing YAML special characters.
func formatYAMLString(s string) string {
	if s == "" {
		return `""`
	}
	if s == "true" || s == "false" || s == "null" || s == "yes" || s == "no" {
		return fmt.Sprintf("%q", s)
	}
	// Check if string contains special chars or control whitespace (e.g. \r, \n, \t)
	specialChars := ":#[]{},&*!|>'\"%@`~\r\n\t"
	if strings.ContainsAny(s, specialChars) || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return fmt.Sprintf("%q", s)
	}
	// Check if numeric
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return fmt.Sprintf("%q", s)
	}
	return s
}
