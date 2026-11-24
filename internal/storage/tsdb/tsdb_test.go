package tsdb_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/storage/tsdb"
	"github.com/Faqihyugos/monstack-go/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTSDB(t *testing.T) (*tsdb.TSDB, string, func()) {
	t.Helper()
	dir := t.TempDir()

	cfg := config.TSDBConfig{
		Path:      dir,
		Retention: "7d",
		MaxDisk:   "10GB",
	}

	store, err := tsdb.Open(cfg)
	require.NoError(t, err)

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(dir)
	}

	return store, dir, cleanup
}

func TestTSDB_IngestionAndQuery(t *testing.T) {
	store, _, cleanup := newTestTSDB(t)
	defer cleanup()

	baseTime := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	// 1. Ingest single point
	pt1 := models.MetricPoint{
		TargetID:  "target-1",
		Metric:    "probe_success",
		Value:     1.0,
		Timestamp: baseTime,
	}
	err := store.WritePoint(pt1)
	require.NoError(t, err)

	// 2. Ingest batch
	points := []models.MetricPoint{
		{
			TargetID:  "target-1",
			Metric:    "probe_duration_seconds",
			Value:     0.025,
			Timestamp: baseTime,
		},
		{
			TargetID:  "target-1",
			Metric:    "probe_success",
			Value:     1.0,
			Timestamp: baseTime.Add(10 * time.Second),
		},
		{
			TargetID:  "target-1",
			Metric:    "probe_duration_seconds",
			Value:     0.030,
			Timestamp: baseTime.Add(10 * time.Second),
		},
	}
	err = store.WritePoints(points)
	require.NoError(t, err)

	// 3. GetLatestPoint
	latestSuccess, err := store.GetLatestPoint("target-1", "probe_success")
	require.NoError(t, err)
	require.NotNil(t, latestSuccess)
	assert.Equal(t, 1.0, latestSuccess.Value)
	assert.Equal(t, baseTime.Add(10*time.Second), latestSuccess.Timestamp)

	latestDuration, err := store.GetLatestPoint("target-1", "probe_duration_seconds")
	require.NoError(t, err)
	require.NotNil(t, latestDuration)
	assert.Equal(t, 0.030, latestDuration.Value)

	// Non-existent target
	none, err := store.GetLatestPoint("missing-target", "probe_success")
	require.NoError(t, err)
	assert.Nil(t, none)

	// 4. QueryRange for specific metric
	res, err := store.QueryRange("target-1", "probe_success", baseTime, baseTime.Add(15*time.Second), 0)
	require.NoError(t, err)
	assert.Len(t, res, 2)

	// 5. QueryRange target-wide (metric == "")
	allRes, err := store.QueryRange("target-1", "", baseTime, baseTime.Add(15*time.Second), 0)
	require.NoError(t, err)
	assert.Len(t, allRes, 4)
}

func TestTSDB_StepBucketing(t *testing.T) {
	store, _, cleanup := newTestTSDB(t)
	defer cleanup()

	baseTime := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	// Ingest 6 points spaced 5s apart (spanning 30s)
	// Window: 10:00:00 to 10:00:30, with step 10s -> 3 buckets
	// Bucket 0: [10:00:00, 10:00:10) -> sample at 10:00:00 (val 1.0), 10:00:05 (val 2.0) -> latest sample 2.0
	// Bucket 1: [10:00:10, 10:00:20) -> sample at 10:00:10 (val 3.0), 10:00:15 (val 4.0) -> latest sample 4.0
	// Bucket 2: [10:00:20, 10:00:30) -> sample at 10:00:20 (val 5.0), 10:00:25 (val 6.0) -> latest sample 6.0
	var points []models.MetricPoint
	for i := 0; i < 6; i++ {
		points = append(points, models.MetricPoint{
			TargetID:  "target-step",
			Metric:    "cpu_usage",
			Value:     float64(i + 1),
			Timestamp: baseTime.Add(time.Duration(i*5) * time.Second),
		})
	}
	err := store.WritePoints(points)
	require.NoError(t, err)

	// Query with step = 10s
	res, err := store.QueryRange("target-step", "cpu_usage", baseTime, baseTime.Add(30*time.Second), 10*time.Second)
	require.NoError(t, err)
	require.Len(t, res, 3)

	assert.Equal(t, 2.0, res[0].Value)
	assert.Equal(t, 4.0, res[1].Value)
	assert.Equal(t, 6.0, res[2].Value)
}

func TestTSDB_WALReplayAndCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")

	baseTime := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	pt1 := models.MetricPoint{
		TargetID:  "replay-target",
		Metric:    "probe_success",
		Value:     1.0,
		Timestamp: baseTime,
	}
	pt2 := models.MetricPoint{
		TargetID:  "replay-target",
		Metric:    "probe_duration_seconds",
		Value:     0.05,
		Timestamp: baseTime.Add(5 * time.Second),
	}

	line1, err := json.Marshal(pt1)
	require.NoError(t, err)
	line2, err := json.Marshal(pt2)
	require.NoError(t, err)

	// Write valid records plus corrupted trailing incomplete bytes
	corruptedTail := []byte("{\"target_id\":\"replay-target\",\"metric\":\"probe_dur")
	content := append(line1, '\n')
	content = append(content, line2...)
	content = append(content, '\n')
	content = append(content, corruptedTail...)

	err = os.WriteFile(walPath, content, 0644)
	require.NoError(t, err)

	cfg := config.TSDBConfig{
		Path: dir,
	}

	// Open TSDB — it should replay valid points and truncate corrupted tail
	store, err := tsdb.Open(cfg)
	require.NoError(t, err)
	defer store.Close()

	// Verify points were replayed into memory
	pts, err := store.QueryRange("replay-target", "", baseTime, baseTime.Add(10*time.Second), 0)
	require.NoError(t, err)
	require.Len(t, pts, 2)
	assert.Equal(t, "probe_success", pts[0].Metric)
	assert.Equal(t, "probe_duration_seconds", pts[1].Metric)

	// Verify that wal.log was truncated to remove the trailing partial line
	fi, err := os.Stat(walPath)
	require.NoError(t, err)
	expectedValidLen := int64(len(line1) + 1 + len(line2) + 1)
	assert.Equal(t, expectedValidLen, fi.Size(), "corrupted tail should have been truncated")

	// Verify new writes append cleanly after truncated point
	pt3 := models.MetricPoint{
		TargetID:  "replay-target",
		Metric:    "probe_success",
		Value:     1.0,
		Timestamp: baseTime.Add(10 * time.Second),
	}
	err = store.WritePoint(pt3)
	require.NoError(t, err)

	fiAfter, err := os.Stat(walPath)
	require.NoError(t, err)
	assert.Greater(t, fiAfter.Size(), expectedValidLen)
}

func TestTSDB_RetentionPruningAndCompaction(t *testing.T) {
	dir := t.TempDir()
	cfg := config.TSDBConfig{
		Path:      dir,
		Retention: "2h",
	}

	store, err := tsdb.Open(cfg)
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC()
	// Add 3 points: 3h ago (expired), 1h ago (valid), now (valid)
	expiredPt := models.MetricPoint{
		TargetID:  "retention-target",
		Metric:    "probe_success",
		Value:     1.0,
		Timestamp: now.Add(-3 * time.Hour),
	}
	validPt1 := models.MetricPoint{
		TargetID:  "retention-target",
		Metric:    "probe_success",
		Value:     1.0,
		Timestamp: now.Add(-1 * time.Hour),
	}
	validPt2 := models.MetricPoint{
		TargetID:  "retention-target",
		Metric:    "probe_success",
		Value:     1.0,
		Timestamp: now,
	}

	err = store.WritePoints([]models.MetricPoint{expiredPt, validPt1, validPt2})
	require.NoError(t, err)

	walPath := filepath.Join(dir, "wal.log")
	fiBefore, err := os.Stat(walPath)
	require.NoError(t, err)

	// Run Prune with 2h retention
	pruned, err := store.Prune(2 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, pruned, "1 expired point should be pruned")

	// Query in-memory: only 2 points should remain
	pts, err := store.QueryRange("retention-target", "probe_success", now.Add(-5*time.Hour), now.Add(time.Hour), 0)
	require.NoError(t, err)
	require.Len(t, pts, 2)
	assert.Equal(t, validPt1.Timestamp.Unix(), pts[0].Timestamp.Unix())
	assert.Equal(t, validPt2.Timestamp.Unix(), pts[1].Timestamp.Unix())

	// Verify WAL file size shrunk after compaction
	fiAfter, err := os.Stat(walPath)
	require.NoError(t, err)
	assert.Less(t, fiAfter.Size(), fiBefore.Size(), "WAL size should be smaller after compacting expired point")
}

func TestTSDB_Concurrency(t *testing.T) {
	store, _, cleanup := newTestTSDB(t)
	defer cleanup()

	concurrency := 20
	pointsPerWorker := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	startTime := time.Now().UTC()

	for worker := 0; worker < concurrency; worker++ {
		go func(wID int) {
			defer wg.Done()
			for i := 0; i < pointsPerWorker; i++ {
				pt := models.MetricPoint{
					TargetID:  fmt.Sprintf("target-%d", wID),
					Metric:    "probe_success",
					Value:     1.0,
					Timestamp: startTime.Add(time.Duration(i) * time.Millisecond),
				}
				_ = store.WritePoint(pt)
				_, _ = store.GetLatestPoint(fmt.Sprintf("target-%d", wID), "probe_success")
			}
		}(worker)
	}

	wg.Wait()

	// Verify all points ingested correctly
	for worker := 0; worker < concurrency; worker++ {
		targetID := fmt.Sprintf("target-%d", worker)
		pts, err := store.QueryRange(targetID, "probe_success", time.Time{}, time.Time{}, 0)
		require.NoError(t, err)
		assert.Len(t, pts, pointsPerWorker)
	}
}
