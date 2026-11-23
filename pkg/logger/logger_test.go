package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
		wantErr  bool
	}{
		{"debug", slog.LevelDebug, false},
		{"DEBUG", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"invalid", slog.LevelInfo, true},
		{"", slog.LevelInfo, true},
	}

	for _, tt := range tests {
		lvl, err := ParseLevel(tt.input)
		if tt.wantErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, lvl)
		}
	}
}

func TestLogger_Init_JSON(t *testing.T) {
	var buf bytes.Buffer
	_, err := InitWithWriter(&buf, "json", "info")
	require.NoError(t, err)

	Info("server started", "port", 8443)

	var entry map[string]any
	err = json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err, "output must be valid JSON: %s", buf.String())

	assert.Equal(t, "server started", entry["msg"])
	assert.Equal(t, "INFO", entry["level"])
	assert.Equal(t, float64(8443), entry["port"])
	assert.NotEmpty(t, entry["time"])
}

func TestLogger_Init_Text(t *testing.T) {
	var buf bytes.Buffer
	_, err := InitWithWriter(&buf, "text", "info")
	require.NoError(t, err)

	Info("service ping", "host", "example.com")
	out := buf.String()

	assert.Contains(t, out, "level=INFO")
	assert.Contains(t, out, "msg=\"service ping\"")
	assert.Contains(t, out, "host=example.com")
}

func TestLogger_DynamicLevel(t *testing.T) {
	var buf bytes.Buffer
	_, err := InitWithWriter(&buf, "text", "info")
	require.NoError(t, err)

	// In INFO mode, Debug should NOT be written
	Debug("this is debug 1")
	assert.Empty(t, buf.String())

	// Dynamically change level to DEBUG
	err = SetLevel("debug")
	require.NoError(t, err)
	assert.Equal(t, slog.LevelDebug, GetLevel())

	Debug("this is debug 2")
	assert.Contains(t, buf.String(), "this is debug 2")

	// Dynamically change level to WARN
	buf.Reset()
	err = SetLevel("warn")
	require.NoError(t, err)

	Info("this is info, should be suppressed")
	assert.Empty(t, buf.String())

	Warn("this is warning, should be recorded")
	assert.Contains(t, buf.String(), "this is warning")

	Error("this is error, should be recorded")
	assert.Contains(t, buf.String(), "this is error")

	// Test invalid level string returns error
	assert.Error(t, SetLevel("bogus_level"))
}

func TestLogger_WithAttributes(t *testing.T) {
	var buf bytes.Buffer
	_, err := InitWithWriter(&buf, "json", "info")
	require.NoError(t, err)

	scopedLogger := With("target_id", "target-42", "env", "prod")
	scopedLogger.Info("target probed", "latency_ms", 15)

	var entry map[string]any
	err = json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "target-42", entry["target_id"])
	assert.Equal(t, "prod", entry["env"])
	assert.Equal(t, float64(15), entry["latency_ms"])
}

func TestLogger_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	_, err := InitWithWriter(&buf, "json", "info")
	require.NoError(t, err)

	subsystemLogger := WithGroup("prober")
	subsystemLogger.Info("prober initialized", "workers", 10)

	var entry map[string]any
	err = json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	group, ok := entry["prober"].(map[string]any)
	require.True(t, ok, "expected prober group map")
	assert.Equal(t, float64(10), group["workers"])
}

func TestLogger_FileOutput(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "logs", "monstack.log")

	cfg := Config{
		Level:    "info",
		Format:   "json",
		Output:   "file",
		FilePath: logFile,
	}

	l, err := Init(cfg)
	require.NoError(t, err)
	require.NotNil(t, l)

	Info("written to file", "status", "ok")

	err = Close()
	require.NoError(t, err)

	content, err := os.ReadFile(logFile)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(content), "written to file"))

	// Test missing file path error
	badCfg := Config{Output: "file"}
	_, err = Init(badCfg)
	require.Error(t, err)
}

func TestLogger_Concurrency(t *testing.T) {
	var buf bytes.Buffer
	_, err := InitWithWriter(&buf, "json", "debug")
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%5 == 0 {
				_ = SetLevel("info")
			} else if id%5 == 1 {
				_ = SetLevel("debug")
			}
			Info("concurrent info", "goroutine", id)
			Debug("concurrent debug", "goroutine", id)
			Warn("concurrent warn", "goroutine", id)
		}(i)
	}

	wg.Wait()
}
