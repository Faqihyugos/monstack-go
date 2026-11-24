package alerting

import (
	"context"
	"log/slog"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Silencer evaluates whether alerts are suppressed by active silences in metadata storage.
type Silencer struct {
	meta   storage.MetadataStore
	logger *slog.Logger
}

// NewSilencer creates a new Silencer backed by the given MetadataStore.
func NewSilencer(meta storage.MetadataStore, logger *slog.Logger) *Silencer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Silencer{
		meta:   meta,
		logger: logger,
	}
}

// IsSilenced returns true if any active silence matches the targetName and labels at time now.
func (s *Silencer) IsSilenced(ctx context.Context, targetName string, labels map[string]string, now time.Time) bool {
	if s == nil || s.meta == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	silences, err := s.meta.ListActiveSilences(ctx, now)
	if err != nil {
		s.logger.Warn("failed to list active silences", "err", err)
		return false
	}

	for _, sil := range silences {
		if sil.IsActive(now) && sil.Matches(targetName, labels) {
			return true
		}
	}
	return false
}

// CreateSilence validates and stores a new AlertSilence.
func (s *Silencer) CreateSilence(ctx context.Context, silence *models.AlertSilence) error {
	if err := silence.Validate(); err != nil {
		return err
	}
	return s.meta.CreateSilence(ctx, silence)
}

// DeleteSilence removes a silence by ID.
func (s *Silencer) DeleteSilence(ctx context.Context, id string) error {
	return s.meta.DeleteSilence(ctx, id)
}
