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
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// === Constants ===
const (
	// Ring buffer size (MUST be power of 2)
	defaultRingSize = 65536
	ringMask        = defaultRingSize - 1
	maxSlotSize     = 4096 // Max bytes per log entry

	// File flags (configurable O_SYNC)
	fileFlagsAsync = os.O_CREATE | os.O_APPEND | os.O_WRONLY
	fileFlagsSync  = os.O_CREATE | os.O_APPEND | os.O_WRONLY | os.O_SYNC

	// Secure-by-default permissions. Logs may contain PII/PHI even with masking
	// enabled, so the shipped defaults are owner-only: files 0600, dirs 0700.
	// This avoids world-readable log data for a module that markets GDPR/HIPAA/PCI
	// compliance. Operators who need broader access can relax perms out of band.
	logFileMode = os.FileMode(0o600)
	logDirMode  = os.FileMode(0o700)

	// flushDrainTimeout bounds how long Flush waits for the async batch worker to
	// drain the ring buffer before syncing, so a stalled worker cannot hang the
	// caller indefinitely.
	flushDrainTimeout = 5 * time.Second
)

// === Pre-allocated Errors ===
var (
	ErrFileNilEntry    = errors.New("cannot write nil entry")
	ErrFileClosed      = errors.New("log file is closed")
	ErrFileQueueFull   = errors.New("write queue is full")
	ErrFileCircuitOpen = errors.New("circuit breaker open")
	ErrFileRateLimited = errors.New("write rate limited")
	ErrFileLockHeld    = errors.New("file lock held by another process")
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

// === Lock-Free Ring Buffer Slot ===
type ringSlot struct {
	// State: 0=free, 1=writing, 2=ready, 3=consumed
	state atomic.Uint32
	len   int32
	//nolint:unused // cache-line padding to avoid false sharing; layout-significant, must not be removed
	_pad [60]byte // Cache line padding (64 bytes total)
	data [maxSlotSize]byte
}

// Ring buffer with padding to avoid false sharing
type ringBuffer struct {
	//nolint:unused // cache-line padding to avoid false sharing; layout-significant, must not be removed
	_pad1    [64]byte
	writeIdx atomic.Uint64
	//nolint:unused // cache-line padding to avoid false sharing; layout-significant, must not be removed
	_pad2   [64]byte
	readIdx atomic.Uint64
	//nolint:unused // cache-line padding to avoid false sharing; layout-significant, must not be removed
	_pad3      [64]byte
	slots      [defaultRingSize]ringSlot
	lostWrites atomic.Uint64 // Track dropped writes
}

// TryWrite attempts lock-free enqueue
func (rb *ringBuffer) TryWrite(data []byte) bool {
	if len(data) > maxSlotSize {
		return false
	}

	// Get next write slot
	idx := rb.writeIdx.Add(1) - 1
	slot := &rb.slots[idx&ringMask]

	// Fast path: try to claim free slot
	if !slot.state.CompareAndSwap(0, 1) {
		rb.lostWrites.Add(1)
		return false
	}

	// Copy data
	copy(slot.data[:], data)
	atomic.StoreInt32(&slot.len, int32(len(data)))

	// Release for reading
	slot.state.Store(2)
	return true
}

// ConsumeInto reads entries into provided slice (batching support)
func (rb *ringBuffer) ConsumeInto(batch [][]byte, maxWait time.Duration) int {
	count := 0
	deadline := time.Now().Add(maxWait)

	for count < len(batch) && time.Now().Before(deadline) {
		idx := rb.readIdx.Load()
		slot := &rb.slots[idx&ringMask]

		state := slot.state.Load()
		if state != 2 {
			if count > 0 {
				return count // Return what we have
			}
			runtime.Gosched()
			continue
		}

		// Try to claim
		if !slot.state.CompareAndSwap(2, 3) {
			continue
		}

		// Read data
		length := atomic.LoadInt32(&slot.len)
		batch[count] = slot.data[:length]
		count++

		// Release slot
		slot.state.Store(0)
		rb.readIdx.Add(1)
	}

	return count
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

// FileAdapter is a high-throughput, lock-free file output adapter with rotation,
// batching, circuit breaking and rate limiting.
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
	maxBatchSize  int
	batchTimeout  time.Duration
	useOSync      bool

	// Atomic state
	closed      atomic.Bool
	currentSize atomic.Int64

	// File management
	currentFile *os.File
	fileLock    *FileLock  // Cross-platform file lock
	fileMu      sync.Mutex // Only for file operations
	useFlock    bool

	// Lock-free ring buffer
	ring *ringBuffer

	// Batch processing
	batchBuf []byte
	batchMu  sync.Mutex

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

// NewFileAdapter creates hybrid adapter
func NewFileAdapter(path string, config *RotationConfig) (*FileAdapter, error) {
	if config == nil {
		config = DefaultRotationConfig()
	}

	adapter := &FileAdapter{
		path:          path,
		lockPath:      path + ".lock",
		maxSize:       config.MaxSize,
		maxBackups:    config.MaxBackups,
		maxAge:        int(config.MaxAge.Hours() / 24),
		compress:      config.Compress,
		flushInterval: config.FlushInterval,
		maxBatchSize:  config.MaxBatchSize,
		batchTimeout:  config.BatchTimeout,
		useOSync:      config.UseOSync,
		useFlock:      config.UseFlock,
		stopChan:      make(chan struct{}),
		batchBuf:      make([]byte, 0, config.MaxBatchSize*2),
		ring:          &ringBuffer{},
		metrics:       &FileAdapterMetrics{},
		cb:            newCircuitBreaker(config.CircuitThreshold, config.CircuitTimeout),
		rl:            newRateLimiter(config.RateLimit, config.RateLimit > 0),
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
	adapter.startBatchWorker()

	if adapter.flushInterval > 0 {
		adapter.startFlushWorker()
	}

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

// Write - zero-allocation hot path with lock-free ring buffer
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

	buf := *bufPtr
	buf = buf[:0]

	line := (*f.formatter.Load()).Format(entry, buf)
	if len(line) == 0 {
		return nil
	}

	if line[len(line)-1] != '\n' {
		line = append(line, '\n')
	}

	// Lock-free enqueue
	if !f.ring.TryWrite(line) {
		f.metrics.WriteErrors.Add(1)
		f.metrics.LostWrites.Add(1)

		// Fallback to stderr (best-effort; a stderr write error is non-actionable here)
		_, _ = os.Stderr.WriteString("[FALLBACK] ")
		_, _ = os.Stderr.Write(line)
		return ErrFileQueueFull
	}

	f.metrics.WritesTotal.Add(1)
	f.metrics.BytesWritten.Add(int64(len(line)))
	f.metrics.QueueDepth.Add(1)

	return nil
}

// WriteZero - zero-allocation version of Write
func (f *FileAdapter) WriteZero(entry *types.LogEntry) error {
	// For now, WriteZero just calls Write since the file adapter already uses
	// efficient buffering and pooling. In the future, this could be optimized
	// further for zero-allocation scenarios.
	return f.Write(entry)
}

// startBatchWorker processes ring buffer with batching
func (f *FileAdapter) startBatchWorker() {
	f.wg.Add(1)

	go func() {
		defer f.wg.Done()

		// Reusable batch slice
		batch := make([][]byte, 1000)

		for {
			select {
			case <-f.stopChan:
				// Drain remaining entries
				for {
					n := f.ring.ConsumeInto(batch, 10*time.Millisecond)
					if n == 0 {
						break
					}
					f.processBatch(batch[:n])
				}
				return

			default:
				// Consume with timeout
				n := f.ring.ConsumeInto(batch, f.batchTimeout)
				if n > 0 {
					f.processBatch(batch[:n])
					f.metrics.QueueDepth.Add(-int32(n))
				}
			}
		}
	}()
}

// processBatch handles batched writes with circuit breaker
func (f *FileAdapter) processBatch(batch [][]byte) {
	if !f.cb.Allow() {
		f.metrics.CircuitBreaks.Add(1)
		// Drop writes when circuit is open
		for range batch {
			f.metrics.WriteErrors.Add(1)
		}
		return
	}

	f.batchMu.Lock()
	defer f.batchMu.Unlock()

	// Accumulate batch
	f.batchBuf = f.batchBuf[:0]
	for _, entry := range batch {
		f.batchBuf = append(f.batchBuf, entry...)
	}

	// Check rotation
	if f.shouldRotate(int64(len(f.batchBuf))) {
		if err := f.rotateLocked(); err != nil {
			_, _ = os.Stderr.WriteString("Rotation failed: ")
			_, _ = os.Stderr.WriteString(err.Error())
			_, _ = os.Stderr.WriteString("\n")
			f.cb.RecordFailure()
			return
		}
	}

	// Write batch
	f.fileMu.Lock()
	n, err := f.currentFile.Write(f.batchBuf)
	f.fileMu.Unlock()

	if err != nil {
		f.cb.RecordFailure()
		f.metrics.WriteErrors.Add(int64(len(batch)))
		_, _ = os.Stderr.WriteString("Batch write failed: ")
		_, _ = os.Stderr.WriteString(err.Error())
		_, _ = os.Stderr.WriteString("\n")
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

// Close gracefully shuts down
func (f *FileAdapter) Close() error {
	if !f.closed.CompareAndSwap(false, true) {
		return nil
	}

	// Stop workers
	close(f.stopChan)

	// Wait for workers with timeout
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		return errors.New("close timeout")
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
	// Update lost writes from ring buffer
	f.metrics.LostWrites.Store(int64(f.ring.lostWrites.Load()))
	return f.metrics
}

// Flush forces any buffered data to be synced to disk.
//
// Writes are enqueued into the lock-free ring buffer and drained asynchronously
// by the batch worker, so a naive Sync() could race ahead of the worker and
// persist an empty file. Flush therefore first waits (bounded) for the ring to
// fully drain — QueueDepth returns to zero once the worker has written every
// enqueued entry to the underlying file handle — and only then fsyncs. This
// gives Flush its documented "prior writes are durably on disk" contract.
func (f *FileAdapter) Flush() error {
	if f.closed.Load() {
		return ErrFileClosed
	}

	// Wait for the batch worker to drain all in-flight entries to the file
	// handle. Bounded so a stalled worker (e.g. open circuit breaker) cannot
	// hang the caller forever.
	deadline := time.Now().Add(flushDrainTimeout)
	for f.metrics.QueueDepth.Load() > 0 {
		if time.Now().After(deadline) {
			break
		}
		runtime.Gosched()
	}

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
