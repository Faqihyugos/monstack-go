package tsdb

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Prune removes points older than the given retention duration from in-memory series
// and compacts the WAL file via temporary file write and atomic rename.
func (s *TSDB) Prune(retention time.Duration) (int, error) {
	if retention <= 0 {
		dur, err := s.cfg.RetentionDuration()
		if err != nil || dur <= 0 {
			return 0, nil
		}
		retention = dur
	}

	cutoff := time.Now().UTC().Add(-retention)
	prunedCount := 0

	s.mu.RLock()
	var allSeries []*Series
	for _, srs := range s.series {
		allSeries = append(allSeries, srs)
	}
	s.mu.RUnlock()

	// 1. Prune expired points from in-memory slices
	var survivingPoints []models.MetricPoint
	for _, srs := range allSeries {
		srs.mu.Lock()
		n := len(srs.points)
		if n > 0 {
			idx := sort.Search(n, func(i int) bool {
				return !srs.points[i].Timestamp.Before(cutoff)
			})
			if idx > 0 {
				prunedCount += idx
				srs.points = srs.points[idx:]
			}
			survivingPoints = append(survivingPoints, srs.points...)
		}
		srs.mu.Unlock()
	}

	// 2. Compact WAL file if active
	if s.wal != nil {
		if err := s.compactWAL(survivingPoints); err != nil {
			return prunedCount, fmt.Errorf("failed to compact WAL: %w", err)
		}
	}

	return prunedCount, nil
}

// compactWAL rewrites wal.log using only surviving points via atomic rename.
func (s *TSDB) compactWAL(surviving []models.MetricPoint) error {
	s.wal.mu.Lock()
	defer s.wal.mu.Unlock()

	tmpPath := filepath.Join(s.dir, "wal.log.tmp")
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create temporary WAL compaction file: %w", err)
	}

	w := bufio.NewWriter(f)
	for _, pt := range surviving {
		line, err := json.Marshal(pt)
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("failed to marshal point during compaction: %w", err)
		}
		if _, err := w.Write(line); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}

	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	_ = f.Close()

	// Close old WAL file handle
	if s.wal.file != nil {
		_ = s.wal.writer.Flush()
		_ = s.wal.file.Sync()
		_ = s.wal.file.Close()
	}

	// Atomically replace wal.log
	walPath := filepath.Join(s.dir, "wal.log")
	if err := os.Rename(tmpPath, walPath); err != nil {
		return fmt.Errorf("failed to rename compacted WAL file: %w", err)
	}

	// Reopen compacted WAL file in append mode
	newF, err := os.OpenFile(walPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to reopen compacted WAL file: %w", err)
	}

	s.wal.file = newF
	s.wal.writer = bufio.NewWriter(newF)

	return nil
}
