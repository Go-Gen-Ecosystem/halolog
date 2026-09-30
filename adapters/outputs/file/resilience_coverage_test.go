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
// @author Admilson B. F. Cossa

package file

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// Circuit breaker coverage
// ============================================================================

func TestCircuitBreaker_ClosedAllowsAndOpensOnFailures(t *testing.T) {
	cb := newCircuitBreaker(3, 50*time.Millisecond)

	// Closed circuit allows traffic.
	if !cb.Allow() {
		t.Fatal("fresh circuit should allow")
	}

	// Record failures up to the threshold to trip it open.
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.state.Load() != 2 {
		t.Fatalf("circuit should be open (2) after threshold failures, got %d", cb.state.Load())
	}

	// Open circuit denies traffic before the timeout elapses.
	if cb.Allow() {
		t.Error("open circuit should deny before timeout")
	}
}

func TestCircuitBreaker_OpenToHalfOpenAfterTimeout(t *testing.T) {
	cb := newCircuitBreaker(1, 10*time.Millisecond)

	cb.RecordFailure() // trips open (threshold 1)
	if cb.state.Load() != 2 {
		t.Fatalf("expected open state, got %d", cb.state.Load())
	}

	// Wait past the timeout; the next Allow transitions open -> half-open.
	time.Sleep(20 * time.Millisecond)
	if !cb.Allow() {
		t.Fatal("after timeout the circuit should allow a probe (half-open)")
	}
	if cb.state.Load() != 1 {
		t.Errorf("expected half-open (1) after timeout probe, got %d", cb.state.Load())
	}
}

func TestCircuitBreaker_HalfOpenLimitsConcurrency(t *testing.T) {
	cb := newCircuitBreaker(1, 5*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(10 * time.Millisecond)

	// The first Allow after the timeout is the open->half-open transition probe
	// (case 2): it returns true WITHOUT consuming a half-open slot. Every
	// subsequent Allow hits the half-open branch (case 1) and is capped by
	// halfOpenMax (3). So a long burst is bounded and eventually returns false.
	allowed := 0
	sawDenied := false
	for i := 0; i < 20; i++ {
		if cb.Allow() {
			allowed++
		} else {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Error("half-open concurrency must eventually deny once the slot cap is reached")
	}
	// The transition probe plus the halfOpenMax slots bound the total allowed.
	if allowed == 0 || allowed >= 20 {
		t.Errorf("half-open should allow a bounded, non-zero number of calls, got %d", allowed)
	}
}

func TestCircuitBreaker_HalfOpenClosesAfterSuccesses(t *testing.T) {
	cb := newCircuitBreaker(1, 5*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(10 * time.Millisecond)
	cb.Allow() // -> half-open

	// 10 successes in half-open close the circuit and reset counters.
	for i := 0; i < 10; i++ {
		cb.RecordSuccess()
	}
	if cb.state.Load() != 0 {
		t.Errorf("circuit should be closed (0) after 10 half-open successes, got %d", cb.state.Load())
	}
	if cb.failures.Load() != 0 {
		t.Error("failures should reset to 0 when circuit closes")
	}
}

func TestCircuitBreaker_RecordFailureUpdatesTimestamp(t *testing.T) {
	cb := newCircuitBreaker(5, time.Second)
	before := time.Now().UnixNano()
	cb.RecordFailure()
	if cb.lastFailTime.Load() < before {
		t.Error("RecordFailure should update lastFailTime to now")
	}
	if cb.failures.Load() != 1 {
		t.Errorf("failures = %d, want 1", cb.failures.Load())
	}
}

// ============================================================================
// Rate limiter coverage
// ============================================================================

func TestRateLimiter_DisabledAlwaysAllows(t *testing.T) {
	// enabled=false yields a permissive limiter.
	rl := newRateLimiter(0, false)
	for i := 0; i < 100; i++ {
		if !rl.Allow() {
			t.Fatal("disabled rate limiter must always allow")
		}
	}

	// tokensPerSec <= 0 with enabled=true is also treated as disabled.
	rl2 := newRateLimiter(0, true)
	if !rl2.Allow() {
		t.Error("rate limiter with 0 tokens/sec should be disabled and allow")
	}
}

func TestRateLimiter_ConsumesTokensThenRefuses(t *testing.T) {
	rl := newRateLimiter(3, true)

	// Exactly 3 tokens are available initially.
	allowed := 0
	for i := 0; i < 3; i++ {
		if rl.Allow() {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("expected 3 initial tokens, consumed %d", allowed)
	}

	// The 4th immediate request has no tokens and no elapsed second, so it fails.
	if rl.Allow() {
		t.Error("4th immediate request should be refused (bucket empty)")
	}
}

func TestRateLimiter_RefillsAfterOneSecond(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping refill timing test in -short mode")
	}
	rl := newRateLimiter(2, true)

	// Drain the bucket.
	rl.Allow()
	rl.Allow()
	if rl.Allow() {
		t.Fatal("bucket should be empty after draining")
	}

	// Force the refill branch by backdating lastRefill more than one second.
	rl.lastRefill.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if !rl.Allow() {
		t.Error("after a simulated 1s+ elapse the bucket should refill and allow")
	}
}

// ============================================================================
// Buffer pool tier selection
// ============================================================================

func TestBufferPools_TierSelection(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantCap int
	}{
		{"small", 100, 256},
		{"small boundary", 256, 256},
		{"medium", 500, 1024},
		{"medium boundary", 1024, 1024},
		{"large", 4096, 4096},
		{"oversized", 100000, 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := getBuffer(tt.size)
			if buf == nil {
				t.Fatal("getBuffer returned nil")
			}
			if cap(*buf) != tt.wantCap {
				t.Errorf("getBuffer(%d) cap = %d, want %d", tt.size, cap(*buf), tt.wantCap)
			}
			// Round-trip through putBuffer must reset length and not panic.
			*buf = append(*buf, 'x', 'y', 'z')
			putBuffer(buf)
		})
	}

	// putBuffer(nil) is a guarded no-op.
	putBuffer(nil)

	// A buffer with a non-standard capacity is dropped by putBuffer (default case).
	odd := make([]byte, 0, 777)
	putBuffer(&odd)
}

// ============================================================================
// Queue edge cases (the long-line, bounded-queue, and overflow cases are in
// reliability_regression_test.go)
// ============================================================================

// Flush returns only once every queued line is in the file, in queue order.
func TestQueue_FlushWritesLinesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order.log")
	adapter, err := NewFileAdapter(path, nil)
	if err != nil {
		t.Fatalf("NewFileAdapter: %v", err)
	}
	defer func() { _ = adapter.Close() }()

	for i := range 3 {
		if err := adapter.Write(newTestEntry(fmt.Sprintf("order seq=%d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := adapter.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var order []string
	for _, line := range readAllLines(t, path) {
		if i := strings.Index(line, "order seq="); i >= 0 {
			order = append(order, line[i:])
		}
	}
	if want := []string{"order seq=0", "order seq=1", "order seq=2"}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("after Flush the file holds %q, want %q", order, want)
	}
}
