package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// StaticFileDiscoverer loads targets dynamically from an external YAML or JSON file.
type StaticFileDiscoverer struct {
	path     string
	interval time.Duration
	logger   *slog.Logger
}

// NewStaticFileDiscoverer creates a static file discoverer from config.
func NewStaticFileDiscoverer(cfg map[string]any, logger *slog.Logger) (*StaticFileDiscoverer, error) {
	p := getStr(cfg, "path", "file")
	if p == "" {
		return nil, errors.New("static discovery requires 'path'")
	}
	return &StaticFileDiscoverer{
		path:     p,
		interval: getDuration(cfg, 30*time.Second, "refresh_interval", "interval"),
		logger:   logger,
	}, nil
}

func (s *StaticFileDiscoverer) Name() string            { return "static:" + filepath.Base(s.path) }
func (s *StaticFileDiscoverer) Interval() time.Duration { return s.interval }

type staticTargetEntry struct {
	Name       string            `json:"name" yaml:"name"`
	Type       string            `json:"type" yaml:"type"`
	Target     string            `json:"target" yaml:"target"`
	IntervalMs int               `json:"interval_ms" yaml:"interval_ms"`
	TimeoutMs  int               `json:"timeout_ms" yaml:"timeout_ms"`
	Labels     map[string]string `json:"labels" yaml:"labels"`
}

// Discover reads and parses the external YAML or JSON target list file.
func (s *StaticFileDiscoverer) Discover(ctx context.Context) ([]*models.Target, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read static discovery file %s: %w", s.path, err)
	}

	var entries []staticTargetEntry
	ext := strings.ToLower(filepath.Ext(s.path))
	if ext == ".json" {
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("failed to parse JSON discovery file: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("failed to parse YAML discovery file: %w", err)
		}
	}

	var targets []*models.Target
	for _, e := range entries {
		if e.Name == "" || e.Target == "" {
			continue
		}
		t := models.NewTarget(e.Name, models.TargetType(e.Type), e.Target, e.IntervalMs, e.TimeoutMs)
		t.ID = e.Name
		for k, v := range e.Labels {
			t.Labels[k] = v
		}
		t.Labels["discovered_by"] = "static_file"
		targets = append(targets, t)
	}
	return targets, nil
}
