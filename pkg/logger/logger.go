package logger

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Config defines logger initialization options.
type Config struct {
	Level    string `json:"level" yaml:"level"`
	Format   string `json:"format" yaml:"format"`
	Output   string `json:"output" yaml:"output"`
	FilePath string `json:"file_path" yaml:"file_path"`
}

var (
	mu           sync.RWMutex
	globalLogger *slog.Logger
	levelVar     = new(slog.LevelVar)
	fileCloser   io.Closer
)

func init() {
	levelVar.Set(slog.LevelInfo)
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelVar,
	})
	globalLogger = slog.New(handler)
	slog.SetDefault(globalLogger)
}

// ParseLevel parses a level string (debug, info, warn/warning, error) into slog.Level.
func ParseLevel(lvl string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level: %s", lvl)
	}
}

// Init initializes the global logger based on Config.
func Init(cfg Config) (*slog.Logger, error) {
	mu.Lock()
	defer mu.Unlock()

	lvl, err := ParseLevel(cfg.Level)
	if err != nil {
		lvl = slog.LevelInfo
	}
	levelVar.Set(lvl)

	var writer io.Writer
	switch strings.ToLower(strings.TrimSpace(cfg.Output)) {
	case "stderr":
		writer = os.Stderr
	case "file":
		if cfg.FilePath == "" {
			return nil, errors.New("file path must be specified when output is 'file'")
		}
		if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}
		f, err := os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		if fileCloser != nil {
			_ = fileCloser.Close()
		}
		fileCloser = f
		writer = f
	default:
		writer = os.Stdout
	}

	return setupHandlerWithWriterLocked(writer, cfg.Format, lvl)
}

// InitWithWriter initializes the logger with a custom io.Writer (e.g. for testing).
func InitWithWriter(w io.Writer, format, levelStr string) (*slog.Logger, error) {
	mu.Lock()
	defer mu.Unlock()

	lvl, err := ParseLevel(levelStr)
	if err != nil {
		lvl = slog.LevelInfo
	}
	levelVar.Set(lvl)
	return setupHandlerWithWriterLocked(w, format, lvl)
}

func setupHandlerWithWriterLocked(w io.Writer, format string, lvl slog.Level) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{
		Level:     levelVar,
		AddSource: (lvl == slog.LevelDebug),
	}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		handler = slog.NewJSONHandler(w, opts)
	}

	globalLogger = slog.New(handler)
	slog.SetDefault(globalLogger)
	return globalLogger, nil
}

// SetLevel dynamically updates the logging level at runtime across all logger instances.
func SetLevel(levelStr string) error {
	lvl, err := ParseLevel(levelStr)
	if err != nil {
		return err
	}
	levelVar.Set(lvl)
	return nil
}

// GetLevel returns the current dynamic log level.
func GetLevel() slog.Level {
	return levelVar.Level()
}

// Get returns the current global slog.Logger.
func Get() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return globalLogger
}

// Close closes any open file handles associated with the logger.
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if fileCloser != nil {
		err := fileCloser.Close()
		fileCloser = nil
		return err
	}
	return nil
}

// With returns a new Logger that includes the given attributes in each log output.
func With(args ...any) *slog.Logger {
	return Get().With(args...)
}

// WithGroup returns a new Logger that starts a group with the given name.
func WithGroup(name string) *slog.Logger {
	return Get().WithGroup(name)
}

// Debug logs at LevelDebug on the global logger.
func Debug(msg string, args ...any) { Get().Debug(msg, args...) }

// Info logs at LevelInfo on the global logger.
func Info(msg string, args ...any) { Get().Info(msg, args...) }

// Warn logs at LevelWarn on the global logger.
func Warn(msg string, args ...any) { Get().Warn(msg, args...) }

// Error logs at LevelError on the global logger.
func Error(msg string, args ...any) { Get().Error(msg, args...) }
