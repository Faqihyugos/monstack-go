package tsdb

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yourusername/monstack-go/internal/config"
	"github.com/yourusername/monstack-go/internal/storage"
	"github.com/yourusername/monstack-go/pkg/models"
)

// seriesKey creates a composite lookup key for a target and metric.
func seriesKey(targetID, metric string) string {
	return targetID + ":" + metric
}

// Series holds a chronologically ordered slice of points for a single metric.
type Series struct {
	mu     sync.RWMutex
	points []models.MetricPoint
}

// TSDB implements the storage.TSDB interface with in-memory buffering and WAL durability.
type TSDB struct {
	mu       sync.RWMutex
	series   map[string]*Series             // key: "targetID:metric"
	byTarget map[string]map[string]struct{} // targetID -> set of metric names
	wal      *WAL
	dir      string
	cfg      config.TSDBConfig
}

// Ensure TSDB implements storage.TSDB.
var _ storage.TSDB = (*TSDB)(nil)

// Open initializes the TSDB engine, performs WAL crash recovery replay, and opens WAL for appends.
func Open(cfg config.TSDBConfig) (*TSDB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("tsdb path cannot be empty")
	}

	store := &TSDB{
		series:   make(map[string]*Series),
		byTarget: make(map[string]map[string]struct{}),
		dir:      cfg.Path,
		cfg:      cfg,
	}

	// 1. Replay historical WAL log into memory before accepting new writes
	if err := Replay(cfg.Path, func(pt models.MetricPoint) {
		store.insertInMemory(pt)
	}); err != nil {
		return nil, fmt.Errorf("failed during WAL replay: %w", err)
	}

	// 2. Open WAL append handle
	wal, err := OpenWAL(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL: %w", err)
	}
	store.wal = wal

	return store, nil
}

// New is an alias for Open for backwards compatibility.
func New(cfg config.TSDBConfig) (*TSDB, error) {
	return Open(cfg)
}

// insertInMemory inserts a point into the in-memory series map, maintaining timestamp ordering.
func (s *TSDB) insertInMemory(pt models.MetricPoint) {
	key := seriesKey(pt.TargetID, pt.Metric)

	s.mu.Lock()
	srs, ok := s.series[key]
	if !ok {
		srs = &Series{}
		s.series[key] = srs
		if s.byTarget[pt.TargetID] == nil {
			s.byTarget[pt.TargetID] = make(map[string]struct{})
		}
		s.byTarget[pt.TargetID][pt.Metric] = struct{}{}
	}
	s.mu.Unlock()

	srs.mu.Lock()
	defer srs.mu.Unlock()

	n := len(srs.points)
	if n == 0 || !pt.Timestamp.Before(srs.points[n-1].Timestamp) {
		srs.points = append(srs.points, pt)
	} else {
		// Out-of-order point: insert in sorted position
		idx := sort.Search(n, func(i int) bool {
			return !srs.points[i].Timestamp.Before(pt.Timestamp)
		})
		srs.points = append(srs.points, models.MetricPoint{})
		copy(srs.points[idx+1:], srs.points[idx:])
		srs.points[idx] = pt
	}
}

// WritePoint validates, persists to WAL, and inserts a metric point into memory.
func (s *TSDB) WritePoint(point models.MetricPoint) error {
	if point.Timestamp.IsZero() {
		point.Timestamp = time.Now().UTC()
	} else {
		point.Timestamp = point.Timestamp.UTC()
	}

	if err := point.Validate(); err != nil {
		return fmt.Errorf("%w: %v", storage.ErrInvalidData, err)
	}

	if s.wal != nil {
		if err := s.wal.Append(point); err != nil {
			return fmt.Errorf("failed to append point to WAL: %w", err)
		}
	}

	s.insertInMemory(point)
	return nil
}

// WritePoints persists a batch of metric points to WAL and inserts into memory.
func (s *TSDB) WritePoints(points []models.MetricPoint) error {
	if len(points) == 0 {
		return nil
	}

	now := time.Now().UTC()
	prepared := make([]models.MetricPoint, len(points))
	for i, pt := range points {
		if pt.Timestamp.IsZero() {
			pt.Timestamp = now
		} else {
			pt.Timestamp = pt.Timestamp.UTC()
		}
		if err := pt.Validate(); err != nil {
			return fmt.Errorf("%w: invalid point at index %d: %v", storage.ErrInvalidData, i, err)
		}
		prepared[i] = pt
	}

	if s.wal != nil {
		if err := s.wal.AppendBatch(prepared); err != nil {
			return fmt.Errorf("failed to append batch to WAL: %w", err)
		}
	}

	for _, pt := range prepared {
		s.insertInMemory(pt)
	}

	return nil
}

// filterRange returns points within [start, end] inclusive using binary search.
func filterRange(points []models.MetricPoint, start time.Time, end time.Time, hasEndFilter bool) []models.MetricPoint {
	n := len(points)
	if n == 0 {
		return []models.MetricPoint{}
	}

	startIdx := 0
	if !start.IsZero() {
		startIdx = sort.Search(n, func(i int) bool {
			return !points[i].Timestamp.Before(start)
		})
		if startIdx >= n {
			return []models.MetricPoint{}
		}
	}

	endIdx := n
	if hasEndFilter {
		endIdx = sort.Search(n, func(i int) bool {
			return points[i].Timestamp.After(end)
		})
	}

	if startIdx >= endIdx {
		return []models.MetricPoint{}
	}

	res := make([]models.MetricPoint, endIdx-startIdx)
	copy(res, points[startIdx:endIdx])
	return res
}

// downsampleBucket aggregates points into [t, t+step) buckets, selecting the latest sample per bucket.
func downsampleBucket(points []models.MetricPoint, start, end time.Time, step time.Duration) []models.MetricPoint {
	if len(points) == 0 || step <= 0 {
		return points
	}

	if start.IsZero() {
		start = points[0].Timestamp
	}
	if end.IsZero() {
		end = points[len(points)-1].Timestamp
	}

	var bucketed []models.MetricPoint
	ptIdx := 0
	n := len(points)

	for bStart := start; bStart.Before(end) || bStart.Equal(end); bStart = bStart.Add(step) {
		bEnd := bStart.Add(step)
		var latestInBucket *models.MetricPoint

		for ptIdx < n {
			pt := points[ptIdx]
			if pt.Timestamp.Before(bStart) {
				ptIdx++
				continue
			}
			if !pt.Timestamp.Before(bEnd) {
				// Reached points outside current bucket
				break
			}
			latestInBucket = &points[ptIdx]
			ptIdx++
		}

		if latestInBucket != nil {
			bucketed = append(bucketed, *latestInBucket)
		}
	}

	if bucketed == nil {
		bucketed = []models.MetricPoint{}
	}
	return bucketed
}

// QueryRange returns points for targetID and metric within [start, end], optionally downsampled by step.
func (s *TSDB) QueryRange(targetID string, metric string, start, end time.Time, step time.Duration) ([]models.MetricPoint, error) {
	hasStartFilter := !start.IsZero()
	if hasStartFilter {
		start = start.UTC()
	} else {
		start = time.Time{}
	}

	hasEndFilter := !end.IsZero()
	if hasEndFilter {
		end = end.UTC()
	}

	if hasStartFilter && hasEndFilter && start.After(end) {
		return []models.MetricPoint{}, nil
	}

	if metric != "" {
		key := seriesKey(targetID, metric)
		s.mu.RLock()
		srs, ok := s.series[key]
		s.mu.RUnlock()
		if !ok {
			return []models.MetricPoint{}, nil
		}

		srs.mu.RLock()
		raw := filterRange(srs.points, start, end, hasEndFilter)
		srs.mu.RUnlock()

		if step <= 0 {
			return raw, nil
		}
		return downsampleBucket(raw, start, end, step), nil
	}

	// metric == "": retrieve all metrics registered under targetID
	s.mu.RLock()
	metricsMap := s.byTarget[targetID]
	var seriesList []*Series
	for m := range metricsMap {
		if srs, exists := s.series[seriesKey(targetID, m)]; exists {
			seriesList = append(seriesList, srs)
		}
	}
	s.mu.RUnlock()

	var allRaw []models.MetricPoint
	for _, srs := range seriesList {
		srs.mu.RLock()
		pts := filterRange(srs.points, start, end, hasEndFilter)
		srs.mu.RUnlock()
		allRaw = append(allRaw, pts...)
	}

	sort.Slice(allRaw, func(i, j int) bool {
		return allRaw[i].Timestamp.Before(allRaw[j].Timestamp)
	})

	if step <= 0 {
		return allRaw, nil
	}

	// Downsample per metric series, then combine
	byMetric := make(map[string][]models.MetricPoint)
	for _, pt := range allRaw {
		byMetric[pt.Metric] = append(byMetric[pt.Metric], pt)
	}
	var result []models.MetricPoint
	for _, pts := range byMetric {
		downsampled := downsampleBucket(pts, start, end, step)
		result = append(result, downsampled...)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})

	return result, nil
}

// GetLatestPoint returns the most recently ingested point for targetID and metric.
func (s *TSDB) GetLatestPoint(targetID string, metric string) (*models.MetricPoint, error) {
	key := seriesKey(targetID, metric)
	s.mu.RLock()
	srs, ok := s.series[key]
	s.mu.RUnlock()
	if !ok {
		return nil, nil
	}

	srs.mu.RLock()
	defer srs.mu.RUnlock()

	n := len(srs.points)
	if n == 0 {
		return nil, nil
	}

	cp := srs.points[n-1]
	return &cp, nil
}

// Close closes the underlying WAL file descriptor.
func (s *TSDB) Close() error {
	if s.wal != nil {
		return s.wal.Close()
	}
	return nil
}
