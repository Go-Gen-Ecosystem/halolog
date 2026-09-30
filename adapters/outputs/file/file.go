// Copyright 2025 Admilson B. F. Cossa
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package adapters provides output adapters
// Author: Admilson B. F. Cossa

package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// === Constants ===
const (
	// File flags (configurable O_SYNC)
	fileFlagsAsync = os.O_CREATE | os.O_APPEND | os.O_WRONLY
	fileFlagsSync  = os.O_CREATE | os.O_APPEND | os.O_WRONLY | os.O_SYNC

	// Secure-by-default permissions. Logs may contain PII/PHI even with masking
	// enabled, so the shipped defaults are owner-only: files 0600, dirs 0700.
	// This avoids world-readable log data for a module that markets GDPR/HIPAA/PCI
	// compliance. Operators who need broader access can relax perms out of band.
	logFileMode = os.FileMode(0o600)
	logDirMode  = os.FileMode(0o700)

	// closeTimeout bounds how long Close waits for the writer and the
	// compressor to finish.
	closeTimeout = 30 * time.Second

	// flushDrainTimeout bounds how long Flush waits for the writer to write
	// every queued line and sync, so a stalled writer cannot hang the caller.
	flushDrainTimeout = 5 * time.Second

	// maxRetainedQueueBytes caps each of the two queue buffers kept for
	// reuse. A full default queue of typical lines (65,536 of about 100 bytes)
	// fits, so sustained load reuses its buffers instead of reallocating them;
	// a burst of much longer lines is not pinned for the life of the adapter.
	maxRetainedQueueBytes = 8 << 20

	// compressQueueSize bounds the rotated files waiting for compression and
	// cleanup; a rotation waits when it is full.
	compressQueueSize = 64

	// rotationRetryDelay spaces out rotation attempts after a failed one, so a
	// rename that keeps failing does not run, and report, on every write.
	rotationRetryDelay = time.Second
)

// === Pre-allocated Errors ===
var (
	ErrFileNilEntry    = errors.New("cannot write nil entry")
	ErrFileClosed      = errors.New("log file is closed")
	ErrFileQueueFull   = errors.New("write queue is full")
	ErrFileCircuitOpen = errors.New("circuit breaker open")
	ErrFileRateLimited = errors.New("write rate limited")
	ErrFileLockHeld    = errors.New("file lock held by another process")

	errFlushTimeout = errors.New("flush timeout")
	errCloseTimeout = errors.New("close timeout")
)

// === Tiered Buffer Pools ===
var (
	smallPool  = sync.Pool{New: func() interface{} { b := make([]byte, 0, 256); return &b }}
	mediumPool = sync.Pool{New: func() interface{} { b := make([]byte, 0, 1024); return &b }}
	largePool  = sync.Pool{New: func() interface{} { b := make([]byte, 0, 4096); return &b }}
)

func getBuffer(size int) *[]byte {
	switch {
	case size <= 256:
		return smallPool.Get().(*[]byte)
	case size <= 1024:
		return mediumPool.Get().(*[]byte)
	default:
		return largePool.Get().(*[]byte)
	}
}

func putBuffer(buf *[]byte) {
	if buf == nil {
		return
	}
	*buf = (*buf)[:0]
	switch cap(*buf) {
	case 256:
		smallPool.Put(buf)
	case 1024:
		mediumPool.Put(buf)
	case 4096:
		largePool.Put(buf)
	}
}

// FileAdapterMetrics holds atomic counters for file adapter observability.
//
//nolint:revive // exported name intentionally kept for a stable public API; renaming to AdapterMetrics would break importers
type FileAdapterMetrics struct {
	WritesTotal     atomic.Int64
	WriteErrors     atomic.Int64
	BytesWritten    atomic.Int64
	RotationsTotal  atomic.Int64
	FlushesTotal    atomic.Int64
	QueueDepth      atomic.Int32
	WriteLatencyP99 atomic.Int64
	CircuitBreaks   atomic.Int64
	RateLimits      atomic.Int64
	LostWrites      atomic.Int64
}

// FileAdapter is a file output adapter: Write queues formatted lines and one
// writer goroutine writes them in batches, with rotation, compression,
// circuit breaking, and rate limiting.
//
//nolint:revive // exported name intentionally kept for a stable public API; renaming to Adapter would break importers
type FileAdapter struct {
	// Immutable config
	path          string
	lockPath      string
	maxSize       int64
	maxBackups    int
	maxAge        int
	compress      bool
	flushInterval time.Duration
	writeTimeout  time.Duration
	useOSync      bool

	// Atomic state
	closed      atomic.Bool
	currentSize atomic.Int64

	// File management
	currentFile *os.File
	fileLock    *FileLock  // Cross-platform file lock
	fileMu      sync.Mutex // Guards currentFile; every file write takes it
	useFlock    bool

	// Lines waiting for the writer, and the writer's flush requests.
	queue    *lineQueue
	flushReq chan chan error

	// Writer-goroutine state: when a failed rotation may be retried.
	nextRotation time.Time

	// Compresses and prunes rotated files, one at a time.
	compressor *compressor

	// Workers
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Resilience
	cb *circuitBreaker
	rl *rateLimiter

	// Metrics
	metrics *FileAdapterMetrics

	// Formatter, swappable at runtime. Held behind an atomic pointer so a
	// concurrent SetFormatter never tears the interface value a writer is
	// reading (an interface write is two words and is not atomic on its own).
	formatter atomic.Pointer[types.Formatter]
}

// NewFileAdapter opens path for appending and starts the writer. A nil config
// uses DefaultRotationConfig; zero tuning fields of a partial config take
// their default values (see RotationConfig).
func NewFileAdapter(path string, config *RotationConfig) (*FileAdapter, error) {
	cfg := withDefaults(config)

	adapter := &FileAdapter{
		path:          path,
		lockPath:      path + ".lock",
		maxSize:       cfg.MaxSize,
		maxBackups:    cfg.MaxBackups,
		maxAge:        int(cfg.MaxAge.Hours() / 24),
		compress:      cfg.Compress,
		flushInterval: cfg.FlushInterval,
		writeTimeout:  cfg.WriteTimeout,
		useOSync:      cfg.UseOSync,
		useFlock:      cfg.UseFlock,
		queue:         newLineQueue(cfg.QueueSize, cfg.MaxBatchSize),
		flushReq:      make(chan chan error),
		stopChan:      make(chan struct{}),
		metrics:       &FileAdapterMetrics{},
		cb:            newCircuitBreaker(cfg.CircuitThreshold, cfg.CircuitTimeout),
		rl:            newRateLimiter(cfg.RateLimit, cfg.RateLimit > 0),
	}
	adapter.SetFormatter(types.NewDefaultConsoleFormatter())

	// Cross-process lock if requested
	if adapter.useFlock {
		if err := adapter.acquireFileLock(); err != nil {
			return nil, err
		}
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, logDirMode); err != nil {
		if adapter.useFlock {
			adapter.releaseFileLock()
		}
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open file
	if err := adapter.openFile(); err != nil {
		if adapter.useFlock {
			adapter.releaseFileLock()
		}
		return nil, err
	}

	// Start workers
	adapter.compressor = newCompressor(compressQueueSize, adapter.rotateAsync)
	adapter.startBatchWorker()
	adapter.startFlushWorker()

	return adapter, nil
}

// acquireFileLock uses cross-platform locking
func (f *FileAdapter) acquireFileLock() error {
	f.fileLock = NewFileLock(f.lockPath)

	// Try to acquire lock with timeout
	if err := f.fileLock.Lock(10 * time.Second); err != nil {
		return fmt.Errorf("failed to acquire file lock: %w", err)
	}

	return nil
}

func (f *FileAdapter) releaseFileLock() {
	if f.fileLock != nil {
		_ = f.fileLock.Unlock()
		f.fileLock = nil
	}
}

// Write formats entry and queues the line for the writer goroutine. When
// QueueSize lines are already waiting it waits up to WriteTimeout for room;
// after that the line is copied to stderr and Write returns ErrFileQueueFull.
func (f *FileAdapter) Write(entry *types.LogEntry) error {
	if entry == nil {
		return ErrFileNilEntry
	}

	if f.closed.Load() {
		return ErrFileClosed
	}

	// Rate limiting check
	if !f.rl.Allow() {
		f.metrics.RateLimits.Add(1)
		return ErrFileRateLimited
	}

	// Format entry
	bufPtr := getBuffer(512)
	defer putBuffer(bufPtr)

	line := (*f.formatter.Load()).Format(entry, (*bufPtr)[:0])
	if len(line) == 0 {
		return nil
	}

	if line[len(line)-1] != '\n' {
		line = append(line, '\n')
	}

	if err := f.queue.Push(line, f.writeTimeout); err != nil {
		if errors.Is(err, ErrFileQueueFull) {
			f.dropLine(line)
		}
		return err
	}

	f.metrics.WritesTotal.Add(1)
	f.metrics.BytesWritten.Add(int64(len(line)))
	f.metrics.QueueDepth.Add(1)
	return nil
}

// dropLine accounts for a line the queue had no room for and copies it to
// stderr, so it is not lost silently.
func (f *FileAdapter) dropLine(line []byte) {
	f.metrics.WriteErrors.Add(1)
	f.metrics.LostWrites.Add(1)
	// Best-effort; a stderr write error is non-actionable here.
	_, _ = os.Stderr.Write(append([]byte("[FALLBACK] "), line...))
}

// reportError writes "what: err" to stderr as one line: the adapter's own
// failures have no other channel.
func reportError(what string, err error) {
	_, _ = os.Stderr.WriteString(what + ": " + err.Error() + "\n")
}

// WriteZero - zero-allocation version of Write
func (f *FileAdapter) WriteZero(entry *types.LogEntry) error {
	// For now, WriteZero just calls Write since the file adapter already uses
	// efficient buffering and pooling. In the future, this could be optimized
	// further for zero-allocation scenarios.
	return f.Write(entry)
}

// startBatchWorker starts the writer. It sleeps until lines are queued, then
// writes every line queued since its last write in one call; Close's final
// drain writes whatever is left.
func (f *FileAdapter) startBatchWorker() {
	f.wg.Add(1)

	go func() {
		defer f.wg.Done()

		for {
			select {
			case <-f.queue.Ready():
				f.drain()

			case reply := <-f.flushReq:
				f.drain()
				reply <- f.syncFile()

			case <-f.stopChan:
				// Close closes the queue before it stops the writer, so one
				// drain empties it.
				f.drain()
				return
			}
		}
	}()
}

// drain writes every queued line.
func (f *FileAdapter) drain() {
	chunk, lines := f.queue.Take()
	if lines == 0 {
		return
	}
	f.writeChunk(chunk, lines)
	f.queue.Done(chunk, lines)
	f.metrics.QueueDepth.Add(-int32(lines))
}

// writeChunk writes queued lines to the file, rotating first when they would
// take it past MaxSize. It runs only on the writer goroutine.
func (f *FileAdapter) writeChunk(chunk []byte, lines int) {
	if !f.cb.Allow() {
		f.metrics.CircuitBreaks.Add(1)
		// Drop writes when circuit is open
		f.metrics.WriteErrors.Add(int64(lines))
		return
	}

	// A failed rotation leaves the log path open, so the lines are written
	// either way; the rotation is retried after rotationRetryDelay.
	if f.shouldRotate(int64(len(chunk))) && !time.Now().Before(f.nextRotation) {
		if err := f.rotateLocked(); err != nil {
			f.nextRotation = time.Now().Add(rotationRetryDelay)
			reportError("Rotation failed", err)
		}
	}

	f.fileMu.Lock()
	n, err := f.currentFile.Write(chunk)
	f.fileMu.Unlock()

	if err != nil {
		f.cb.RecordFailure()
		f.metrics.WriteErrors.Add(int64(lines))
		reportError("Batch write failed", err)
		return
	}

	f.cb.RecordSuccess()
	f.currentSize.Add(int64(n))
	f.metrics.FlushesTotal.Add(1)
}

// startFlushWorker periodically syncs to disk
func (f *FileAdapter) startFlushWorker() {
	f.wg.Add(1)

	go func() {
		defer f.wg.Done()

		ticker := time.NewTicker(f.flushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				f.fileMu.Lock()
				if f.currentFile != nil {
					// Best-effort periodic sync; a transient sync error is retried on the next tick.
					_ = f.currentFile.Sync()
				}
				f.fileMu.Unlock()

			case <-f.stopChan:
				return
			}
		}
	}()
}

// Close refuses new lines, writes every queued line, finishes pending
// compressions, and closes the file.
func (f *FileAdapter) Close() error {
	if !f.closed.CompareAndSwap(false, true) {
		return nil
	}

	// Refuse new lines before stopping the writer, so its final drain leaves
	// nothing behind.
	f.queue.Close()
	close(f.stopChan)

	// Wait for the writer (which may still rotate) and then for compression.
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		f.compressor.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(closeTimeout):
		return errCloseTimeout
	}

	// Final sync (best-effort during shutdown)
	f.fileMu.Lock()
	if f.currentFile != nil {
		_ = f.currentFile.Sync()
		_ = f.currentFile.Close()
		f.currentFile = nil
	}
	f.fileMu.Unlock()

	// Release lock
	if f.useFlock {
		f.releaseFileLock()
	}

	return nil
}

// Name returns the adapter's identifier.
func (f *FileAdapter) Name() string {
	return "FileAdapter"
}

// SetFormatter sets the formatter used to render entries before writing
// (nil is ignored). Safe to call concurrently with writes.
func (f *FileAdapter) SetFormatter(formatter types.Formatter) {
	if formatter == nil {
		return
	}
	f.formatter.Store(&formatter)
}

// Health reports whether the adapter is open and its underlying file is writable.
func (f *FileAdapter) Health() error {
	if f.closed.Load() {
		return ErrFileClosed
	}

	f.fileMu.Lock()
	defer f.fileMu.Unlock()

	if f.currentFile == nil {
		return errors.New("file adapter not initialized")
	}

	// Non-destructive check
	if _, err := f.currentFile.Write([]byte{}); err != nil {
		return fmt.Errorf("file adapter unhealthy: %w", err)
	}

	return nil
}

// Metrics returns a snapshot pointer to the adapter's live metrics counters.
func (f *FileAdapter) Metrics() *FileAdapterMetrics {
	return f.metrics
}

// Flush writes every queued line and syncs the file to disk, so lines written
// before the call are durable when it returns nil. It gives up after
// flushDrainTimeout if the writer is stalled.
func (f *FileAdapter) Flush() error {
	if f.closed.Load() {
		return ErrFileClosed
	}

	reply := make(chan error, 1)
	timer := time.NewTimer(flushDrainTimeout)
	defer timer.Stop()

	select {
	case f.flushReq <- reply:
	case <-f.stopChan:
		return ErrFileClosed
	case <-timer.C:
		return errFlushTimeout
	}

	select {
	case err := <-reply:
		return err
	case <-timer.C:
		return errFlushTimeout
	}
}

// syncFile fsyncs the current file.
func (f *FileAdapter) syncFile() error {
	f.fileMu.Lock()
	defer f.fileMu.Unlock()

	if f.currentFile == nil {
		return nil
	}
	return f.currentFile.Sync()
}

// openFile opens log file with appropriate flags
func (f *FileAdapter) openFile() error {
	flags := fileFlagsAsync
	if f.useOSync {
		flags = fileFlagsSync
	}

	file, err := os.OpenFile(f.path, flags, logFileMode)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	f.currentFile = file
	f.currentSize.Store(stat.Size())
	return nil
}
