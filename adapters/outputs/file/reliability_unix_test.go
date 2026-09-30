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

//go:build unix

package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// An idle adapter must not burn CPU. The writer used to poll its queue in a
// loop, costing more than a core with the default configuration.
func TestFileAdapter_IdleWriterDoesNotSpin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idle.log")
	adapter := newTestAdapter(t, path, DefaultRotationConfig())
	_ = adapter.Write(newTestEntry("idle seq=0"))
	time.Sleep(100 * time.Millisecond)

	start := cpuTime(t)
	time.Sleep(500 * time.Millisecond)
	if used := cpuTime(t) - start; used > 150*time.Millisecond {
		t.Fatalf("the process used %v of CPU during 500ms with an idle adapter", used)
	}
}

// A failed rotation must not drop the lines being written. The batch was
// discarded when the rename failed, and later writes went to a closed file.
func TestFileAdapter_FailedRotationKeepsTheBatch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can rename inside a read-only directory")
	}
	muteStderr(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "ro.log")
	adapter := newTestAdapter(t, path, &RotationConfig{
		MaxSize:      200,
		BatchTimeout: 5 * time.Millisecond,
	})
	// Without write permission on the directory, the rotation rename fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	const lines = 5
	for i := range lines {
		if err := adapter.Write(newTestEntry(fmt.Sprintf("%s ro seq=%d", strings.Repeat("r", 100), i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = adapter.Close()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if got := seqs(readAllLines(t, path), "ro"); len(got) != lines {
		t.Fatalf("%d of %d lines reached the file while rotation kept failing", len(got), lines)
	}
}
