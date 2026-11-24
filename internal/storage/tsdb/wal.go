package tsdb

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// WAL represents the append-only Write-Ahead Log for TSDB persistence.
type WAL struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	writer *bufio.Writer
}

// OpenWAL opens or creates the WAL log file at the given directory.
func OpenWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory %s: %w", dir, err)
	}

	walPath := filepath.Join(dir, "wal.log")
	f, err := os.OpenFile(walPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL file %s: %w", walPath, err)
	}

	return &WAL{
		path:   walPath,
		file:   f,
		writer: bufio.NewWriter(f),
	}, nil
}

// Replay reads the existing WAL file, calls insertFn for every valid MetricPoint,
// and truncates any corrupted or incomplete trailing bytes left over from a crash.
func Replay(dir string, insertFn func(models.MetricPoint)) error {
	walPath := filepath.Join(dir, "wal.log")
	f, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open WAL for replay: %w", err)
	}

	reader := bufio.NewReader(f)
	var lastValidOffset int64
	var currentOffset int64

	for {
		line, readErr := reader.ReadBytes('\n')
		lineLen := int64(len(line))
		currentOffset += lineLen

		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) > 0 {
				var pt models.MetricPoint
				if err := json.Unmarshal(trimmed, &pt); err == nil && pt.Validate() == nil {
					insertFn(pt)
					lastValidOffset = currentOffset
				}
				// If JSON unmarshal fails or validation fails, we do not advance lastValidOffset.
			} else {
				// Empty line, advance valid offset
				lastValidOffset = currentOffset
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			break
		}
	}
	_ = f.Close()

	// Truncate any incomplete trailing bytes if the file was partially written before crash
	if fi, err := os.Stat(walPath); err == nil && fi.Size() > lastValidOffset {
		_ = os.Truncate(walPath, lastValidOffset)
	}

	return nil
}

// Append appends a single MetricPoint as newline-delimited JSON.
func (w *WAL) Append(pt models.MetricPoint) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(pt)
	if err != nil {
		return fmt.Errorf("failed to marshal metric point for WAL: %w", err)
	}

	if _, err := w.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write WAL record: %w", err)
	}
	if err := w.writer.WriteByte('\n'); err != nil {
		return fmt.Errorf("failed to write WAL newline: %w", err)
	}

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL buffer: %w", err)
	}

	return nil
}

// AppendBatch appends multiple MetricPoints atomically as newline-delimited JSON records.
func (w *WAL) AppendBatch(points []models.MetricPoint) error {
	if len(points) == 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	var buf bytes.Buffer
	for _, pt := range points {
		data, err := json.Marshal(pt)
		if err != nil {
			return fmt.Errorf("failed to marshal metric point in batch: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	if _, err := w.writer.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("failed to write WAL batch: %w", err)
	}

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL batch buffer: %w", err)
	}

	return nil
}

// Sync flushes all pending in-memory buffers to disk.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
}

// Close flushes buffers and closes the underlying WAL file descriptor.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	_ = w.writer.Flush()
	_ = w.file.Sync()
	err := w.file.Close()
	w.file = nil
	return err
}
