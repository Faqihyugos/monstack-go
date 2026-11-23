package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadCallback is triggered when a new valid configuration is loaded.
type ReloadCallback func(oldCfg, newCfg *Config) error

// Watcher monitors configuration file modifications on disk.
type Watcher struct {
	configPath string
	current    atomic.Pointer[Config]
	callbacks  []ReloadCallback
	mu         sync.Mutex

	fsWatcher *fsnotify.Watcher
	debounce  time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	doneCh    chan struct{}
	logger    *slog.Logger
}

// NewWatcher initializes a file watcher with initial configuration.
func NewWatcher(path string, initialCfg *Config, logger *slog.Logger) (*Watcher, error) {
	if logger == nil {
		logger = slog.Default()
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path of config: %w", err)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{
		configPath: absPath,
		callbacks:  make([]ReloadCallback, 0),
		fsWatcher:  fsWatcher,
		debounce:   150 * time.Millisecond,
		ctx:        ctx,
		cancel:     cancel,
		doneCh:     make(chan struct{}),
		logger:     logger,
	}
	w.current.Store(initialCfg)

	// Watch the containing directory to survive atomic renames/removals
	dir := filepath.Dir(absPath)
	if err := fsWatcher.Add(dir); err != nil {
		cancel()
		_ = fsWatcher.Close()
		return nil, fmt.Errorf("failed to add watch on config directory %s: %w", dir, err)
	}

	go w.eventLoop()
	return w, nil
}

// OnReload registers a callback executed when configuration reloads successfully.
func (w *Watcher) OnReload(cb ReloadCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

// Config returns the active running configuration.
func (w *Watcher) Config() *Config {
	return w.current.Load()
}

// Reload manually forces a reload from disk (used by API PUT /api/v1/config/reload and SIGHUP).
func (w *Watcher) Reload() (*Config, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	newCfg, err := Load(w.configPath)
	if err != nil {
		w.logger.Error("config reload failed", "path", w.configPath, "err", err)
		return nil, err
	}

	oldCfg := w.current.Swap(newCfg)
	for _, cb := range w.callbacks {
		if err := cb(oldCfg, newCfg); err != nil {
			w.logger.Error("config reload callback error", "err", err)
		}
	}
	w.logger.Info("configuration reloaded successfully", "path", w.configPath)
	return newCfg, nil
}

// Close terminates file watching and releases resources.
func (w *Watcher) Close() error {
	w.cancel()
	err := w.fsWatcher.Close()
	<-w.doneCh
	return err
}

// eventLoop handles file events with debounce and atomic re-registration.
func (w *Watcher) eventLoop() {
	defer close(w.doneCh)

	var (
		debounceTimer *time.Timer
		targetFile    = filepath.Base(w.configPath)
	)

	for {
		select {
		case <-w.ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			// Only act on events targeting our configuration file
			if filepath.Base(event.Name) != targetFile {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(w.debounce, func() {
					// Check if file still exists (may take a few ms after rename)
					if _, err := os.Stat(w.configPath); err == nil {
						if _, err := w.Reload(); err != nil {
							w.logger.Warn("hot-reload failed to apply updated file", "err", err)
						}
					}
				})
			}

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			w.logger.Error("fsnotify watcher encountered error", "err", err)
		}
	}
}
