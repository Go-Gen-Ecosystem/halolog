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

package file

// Regression tests for issue #12: each one reproduces a way the adapter lost
// or withheld lines, or cost far more than it should.

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// muteStderr silences the adapter's own stderr messages (dropped-line
// fallbacks, rotation errors) for the rest of the test.
func muteStderr(t *testing.T) {
	t.Helper()
	_ = captureStderr(t)
}

// newTestAdapter opens an adapter on path and closes it when the test ends.
func newTestAdapter(t *testing.T, path string, cfg *RotationConfig) *FileAdapter {
	t.Helper()
	adapter, err := NewFileAdapter(path, cfg)
	if err != nil {
		t.Fatalf("NewFileAdapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

// captureStderr sends the adapter's stderr messages to a file for the rest of
// the test and returns a function that reads what was written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	capture, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	saved := os.Stderr
	os.Stderr = capture
	t.Cleanup(func() {
		os.Stderr = saved
		_ = capture.Close()
	})
	return func() string {
		b, _ := os.ReadFile(capture.Name())
		return string(b)
	}
}

// waitFor polls cond until it holds or d elapses.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readAllLines returns the lines of path and of every rotated backup of it,
// plain or gzip-compressed.
func readAllLines(t *testing.T, path string) []string {
	t.Helper()
	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var lines []string
	for _, name := range files {
		if strings.HasSuffix(name, ".lock") {
			continue
		}
		fh, err := os.Open(name) //nolint:gosec // G304: test-owned temp path
		if err != nil {
			continue
		}
		var r io.Reader = fh
		if strings.HasSuffix(name, ".gz") {
			zr, err := gzip.NewReader(fh)
			if err != nil {
				_ = fh.Close()
				t.Fatalf("%s is not a readable gzip file: %v", filepath.Base(name), err)
			}
			r = zr
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		_ = fh.Close()
	}
	return lines
}

// seqs collects the numbers N of the "<tag> seq=N" markers in lines.
func seqs(lines []string, tag string) map[int]bool {
	re := regexp.MustCompile(regexp.QuoteMeta(tag) + ` seq=(\d+)`)
	found := map[int]bool{}
	for _, line := range lines {
		if m := re.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			found[n] = true
		}
	}
	return found
}

// A RotationConfig that sets only some fields must still write lines while
// the adapter is open. Zero BatchTimeout used to leave the writer spinning
// without ever taking a line, so the file stayed empty until Close.
func TestFileAdapter_PartialConfigWritesWithoutClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.log")
	adapter := newTestAdapter(t, path, &RotationConfig{
		MaxSize:       10 << 20,
		MaxBackups:    3,
		Compress:      true,
		FlushInterval: 2 * time.Second,
		QueueSize:     10,
	})

	const lines = 20
	for i := range lines {
		if err := adapter.Write(newTestEntry(fmt.Sprintf("partial seq=%d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	var got int
	if !waitFor(2*time.Second, func() bool {
		got = len(seqs(readAllLines(t, path), "partial"))
		return got == lines
	}) {
		t.Fatalf("the open adapter wrote %d of %d lines within 2s", got, lines)
	}
}

// Rotations in the same second must not overwrite one another. Backups were
// named to the second, so a 1 KiB MaxSize lost most lines to renames onto an
// existing backup and to compressions racing on one name.
func TestFileAdapter_RotationKeepsEveryLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rot.log")
	adapter := newTestAdapter(t, path, &RotationConfig{
		MaxSize:      1024,
		Compress:     true,
		BatchTimeout: 5 * time.Millisecond,
	})

	const lines = 600
	for i := range lines {
		if err := adapter.Write(newTestEntry(fmt.Sprintf("rot seq=%d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := seqs(readAllLines(t, path), "rot")
	if len(got) != lines {
		t.Fatalf("%d of %d lines survived %d rotations", len(got), lines, adapter.Metrics().RotationsTotal.Load())
	}
}

// Backups that retention removes before compression reaches them must not be
// reported as failures. The issue's configuration (MaxSize 1024, MaxBackups
// 3, Compress) printed "Compression failed: ... no such file or directory".
func TestFileAdapter_RetentionDoesNotReportCompressionFailures(t *testing.T) {
	stderr := captureStderr(t)
	path := filepath.Join(t.TempDir(), "retain.log")
	adapter := newTestAdapter(t, path, &RotationConfig{
		MaxSize:    1024,
		MaxBackups: 3,
		Compress:   true,
	})
	for i := range 2000 {
		_ = adapter.Write(newTestEntry(fmt.Sprintf("retain seq=%d", i)))
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out := stderr(); strings.Contains(out, "Compression failed") {
		t.Fatalf("stderr reports compression failures:\n%s", firstLines(out, 3))
	}
	backups, _ := filepath.Glob(path + ".*")
	if len(backups) > 3 {
		t.Fatalf("%d backups remain, want at most MaxBackups (3)", len(backups))
	}
	_ = readAllLines(t, path) // every remaining backup must be a readable gzip file
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// After the queue overflows once, later lines must still reach the file. A
// dropped enqueue used to leave a hole the writer waited on until another full
// lap of the queue, and Close discarded everything behind it.
func TestFileAdapter_KeepsWritingAfterQueueOverflow(t *testing.T) {
	muteStderr(t)
	path := filepath.Join(t.TempDir(), "overflow.log")
	cfg := DefaultRotationConfig()
	cfg.MaxSize = 0
	cfg.QueueSize = 1000
	cfg.WriteTimeout = time.Millisecond
	adapter := newTestAdapter(t, path, cfg)

	// Every file write takes fileMu; holding it stalls the writer so the queue
	// fills and starts dropping.
	adapter.fileMu.Lock()
	for i := 0; i < 200000 && adapter.Metrics().LostWrites.Load() == 0; i++ {
		_ = adapter.Write(newTestEntry(fmt.Sprintf("fill seq=%d", i)))
	}
	adapter.fileMu.Unlock()
	if adapter.Metrics().LostWrites.Load() == 0 {
		t.Fatal("the queue never overflowed; the test cannot check recovery")
	}

	const later = 10
	for i := range later {
		_ = adapter.Write(newTestEntry(fmt.Sprintf("after seq=%d", i)))
		time.Sleep(5 * time.Millisecond)
	}
	var got int
	if !waitFor(2*time.Second, func() bool {
		got = len(seqs(readAllLines(t, path), "after"))
		return got == later
	}) {
		t.Fatalf("after an overflow the open adapter wrote %d of %d later lines", got, later)
	}
}

// QueueSize bounds how many lines wait to be written. It used to be ignored in
// favour of a fixed 65,536-slot queue.
func TestFileAdapter_QueueSizeBoundsPendingLines(t *testing.T) {
	muteStderr(t)
	path := filepath.Join(t.TempDir(), "bounded.log")
	cfg := DefaultRotationConfig()
	cfg.MaxSize = 0
	cfg.QueueSize = 50
	cfg.WriteTimeout = time.Millisecond
	adapter := newTestAdapter(t, path, cfg)

	adapter.fileMu.Lock()
	for i := range 60 {
		_ = adapter.Write(newTestEntry(fmt.Sprintf("bounded seq=%d", i)))
	}
	lost := adapter.Metrics().LostWrites.Load()
	adapter.fileMu.Unlock()
	if lost != 10 {
		t.Fatalf("QueueSize 50 with a stalled writer: %d of 60 lines dropped, want 10", lost)
	}
}

// A line longer than 4 KiB, such as one carrying a stack trace, must reach the
// file. Queue slots held at most 4,096 bytes and longer lines went to stderr.
func TestFileAdapter_WritesLinesLongerThan4KiB(t *testing.T) {
	muteStderr(t)
	path := filepath.Join(t.TempDir(), "long.log")
	adapter := newTestAdapter(t, path, DefaultRotationConfig())
	if err := adapter.Write(newTestEntry(strings.Repeat("s", 6000) + " long seq=1")); err != nil {
		t.Errorf("Write of a 6 KB line: %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := seqs(readAllLines(t, path), "long"); !got[1] {
		t.Fatal("the 6 KB line never reached the file")
	}
}

// An idle adapter must not hold memory for a queue it is not using. The fixed
// queue allocated 65,536 slots of 4 KiB, about 270 MB, per adapter.
func TestFileAdapter_IdleAdapterHoldsLittleMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.log")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	adapter := newTestAdapter(t, path, DefaultRotationConfig())
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	runtime.KeepAlive(adapter)
	_ = adapter.Close()
	if grew > 16<<20 {
		t.Fatalf("a new, idle adapter holds %d MiB of heap", grew>>20)
	}
}
