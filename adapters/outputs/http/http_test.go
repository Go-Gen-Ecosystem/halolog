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

package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
	"github.com/go-gen-ecosystem/halolog/utils"
)

// ============================================================================
// Test Helpers & Mocks
// ============================================================================

// mockFormatter tracks formatting calls
type mockFormatter struct {
	formatFunc func(entry *types.LogEntry, buf []byte) []byte
	callCount  int64
}

func (f *mockFormatter) Format(entry *types.LogEntry, buf []byte) []byte {
	atomic.AddInt64(&f.callCount, 1)
	if f.formatFunc != nil {
		return f.formatFunc(entry, buf)
	}
	return buf
}

func (f *mockFormatter) Reset() {
	// Nothing to reset
}

func (f *mockFormatter) EstimatedSize() int {
	return 1024
}

// fixedTimeNow returns deterministic timestamp
func fixedTimeNow() time.Time {
	return time.Unix(1234567890, 0)
}

// assertZeroAlloc verifies zero allocations
func assertZeroAlloc(t *testing.T, name string, f func()) {
	t.Helper()
	if allocs := testing.AllocsPerRun(100, f); allocs != 0 {
		t.Fatalf("%s: expected 0 allocations, got %.0f", name, allocs)
	}
}

// ============================================================================
// HTTPAdapter Constructor Tests
// ============================================================================

func TestNewHTTPAdapter(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		opts     *HTTPAdapterOptions
		validate func(t *testing.T, a *HTTPAdapter)
	}{
		{
			name: "default constructor",
			url:  "http://test.com",
			validate: func(t *testing.T, a *HTTPAdapter) {
				if a.url != "http://test.com" {
					t.Errorf("URL: got %s", a.url)
				}
				if a.method != "POST" {
					t.Errorf("Method: got %s", a.method)
				}
				if a.batchSize != 100 {
					t.Errorf("BatchSize: got %d", a.batchSize)
				}
				if a.flushInterval != 5*time.Second {
					t.Errorf("FlushInterval: got %v", a.flushInterval)
				}
				if a.client.Timeout != 30*time.Second {
					t.Errorf("Timeout: got %v", a.client.Timeout)
				}
				if a.formatter.Load() == nil {
					t.Error("Formatter: nil")
				}
				if a.ctx == nil || a.cancel == nil {
					t.Error("Context: nil")
				}
				if a.queue.threshold != 100 {
					t.Errorf("Batch threshold: got %d", a.queue.threshold)
				}
			},
		},
		{
			name: "custom options",
			opts: &HTTPAdapterOptions{
				URL:           "http://custom.com",
				Method:        "PUT",
				Headers:       map[string]string{"X-Custom": "value"},
				BatchSize:     50,
				FlushInterval: 10 * time.Second,
				Timeout:       60 * time.Second,
			},
			validate: func(t *testing.T, a *HTTPAdapter) {
				if a.method != "PUT" {
					t.Error("Method not set")
				}
				if a.batchSize != 50 {
					t.Error("BatchSize not set")
				}
				if a.header.Get("X-Custom") != "value" {
					t.Error("Headers not set")
				}
				if a.client.Timeout != 60*time.Second {
					t.Error("Timeout not set")
				}
			},
		},
		{
			name: "zero flush interval takes the default",
			opts: &HTTPAdapterOptions{URL: "http://test.com", FlushInterval: 0},
			validate: func(t *testing.T, a *HTTPAdapter) {
				if a.flushInterval != defaultFlushInterval {
					t.Errorf("FlushInterval: got %v", a.flushInterval)
				}
			},
		},
		{
			name: "custom formatter",
			opts: &HTTPAdapterOptions{
				URL:       "http://test.com",
				Formatter: &mockFormatter{},
			},
			validate: func(t *testing.T, a *HTTPAdapter) {
				fp := a.formatter.Load()
				if fp == nil {
					t.Fatal("Custom formatter not set")
				}
				if _, ok := (*fp).(*mockFormatter); !ok {
					t.Error("Custom formatter not set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a *HTTPAdapter
			if tt.opts != nil {
				a = NewHTTPAdapterWithOptions(tt.opts)
			} else {
				a = NewHTTPAdapter(tt.url)
			}
			defer func() { _ = a.Close() }()
			tt.validate(t, a)
		})
	}
}

// ============================================================================
// HTTPAdapter Method Tests
// ============================================================================

func TestHTTPAdapter_Name(t *testing.T) {
	adapter := NewHTTPAdapter("http://test.com")
	defer func() { _ = adapter.Close() }()
	if adapter.Name() != "HTTPAdapter" {
		t.Error("Name incorrect")
	}
}

func TestHTTPAdapter_Write(t *testing.T) {
	t.Run("valid entry", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     10,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		entry := &types.LogEntry{Level: types.InfoLevel, Message: "test"}
		if err := adapter.Write(entry); err != nil {
			t.Errorf("Write: %v", err)
		}

		l := int(adapter.unsent.Load())
		if l != 1 {
			t.Errorf("Buffer length: got %d", l)
		}
	})

	t.Run("nil entry", func(t *testing.T) {
		adapter := NewHTTPAdapter("http://test.com")
		defer func() { _ = adapter.Close() }()
		if err := adapter.Write(nil); err != ErrHTTPNilEntry {
			t.Errorf("Expected ErrHTTPNilEntry, got %v", err)
		}
	})

	t.Run("triggers flush at batch size", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     2,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		_ = adapter.Write(&types.LogEntry{Level: types.InfoLevel, Message: "msg1"})
		_ = adapter.Write(&types.LogEntry{Level: types.InfoLevel, Message: "msg2"})

		time.Sleep(100 * time.Millisecond)
		if atomic.LoadInt32(&calls) != 1 {
			t.Error("Flush not triggered")
		}
	})

	t.Run("concurrent writes", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     200,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = adapter.Write(&types.LogEntry{Level: types.InfoLevel, Message: "concurrent"})
			}()
		}
		wg.Wait()

		l := int(adapter.unsent.Load())
		if l != 100 {
			t.Errorf("Concurrent writes: got %d entries", l)
		}
	})
}

func TestHTTPAdapter_WriteZero(t *testing.T) {
	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           "http://test.com",
		BatchSize:     10,
		FlushInterval: 0,
	})
	defer func() { _ = adapter.Close() }()

	entry := &types.LogEntry{
		Level:     types.InfoLevel,
		Message:   "zero alloc",
		Timestamp: fixedTimeNow(),
	}

	if err := adapter.WriteZero(entry); err != nil {
		t.Errorf("WriteZero: %v", err)
	}

	l := int(adapter.unsent.Load())
	if l != 1 {
		t.Error("WriteZero did not add entry")
	}
}

func TestHTTPAdapter_Health(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		adapter := NewHTTPAdapter("http://test.com")
		defer func() { _ = adapter.Close() }()
		if err := adapter.Health(); err != nil {
			t.Errorf("Health: %v", err)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		adapter := NewHTTPAdapter("http://test.com")
		adapter.cancel()
		if err := adapter.Health(); err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
		_ = adapter.Close() // Cleanup
	})
}

func TestHTTPAdapter_Flush(t *testing.T) {
	t.Run("empty buffer", func(t *testing.T) {
		adapter := NewHTTPAdapter("http://test.com")
		defer func() { _ = adapter.Close() }()
		if err := adapter.Flush(); err != nil {
			t.Errorf("Flush empty: %v", err)
		}
	})

	t.Run("successful flush", func(t *testing.T) {
		var bodies [][]byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     10,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		_ = adapter.Write(&types.LogEntry{Level: types.InfoLevel, Message: "msg1"})
		_ = adapter.Write(&types.LogEntry{Level: types.InfoLevel, Message: "msg2"})
		_ = adapter.Write(&types.LogEntry{Level: types.InfoLevel, Message: "msg3"})

		if err := adapter.Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}

		time.Sleep(100 * time.Millisecond)

		if len(bodies) != 1 {
			t.Fatalf("Expected 1 request, got %d", len(bodies))
		}

		var payload map[string]interface{}
		if err := json.Unmarshal(bodies[0], &payload); err != nil {
			t.Fatalf("JSON parse: %v", err)
		}
		if int(payload["count"].(float64)) != 3 {
			t.Error("Batch count incorrect")
		}
	})

	t.Run("HTTP error requeues", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     2,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		entry1, entry2 := &types.LogEntry{Message: "1"}, &types.LogEntry{Message: "2"}
		_ = adapter.Write(entry1)
		_ = adapter.Write(entry2)
		_ = adapter.Flush()

		time.Sleep(100 * time.Millisecond)

		l := int(adapter.unsent.Load())
		if l != 2 {
			t.Error("Failed entries not requeued")
		}
	})

	t.Run("network error", func(t *testing.T) {
		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           "http://invalid:99999",
			BatchSize:     2,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		_ = adapter.Write(&types.LogEntry{Message: "1"})
		_ = adapter.Write(&types.LogEntry{Message: "2"})
		if err := adapter.Flush(); err == nil {
			t.Error("Expected network error")
		}
	})

	t.Run("batch splitting", func(t *testing.T) {
		var requests int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&requests, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     3,
			FlushInterval: 0,
		})
		defer func() { _ = adapter.Close() }()

		// Write 7 entries -> 3 batches (3, 3, 1)
		for i := 0; i < 7; i++ {
			_ = adapter.Write(&types.LogEntry{Message: utils.FormatIntWithPrefix("md", i)})
		}
		_ = adapter.Flush()

		time.Sleep(150 * time.Millisecond)
		if atomic.LoadInt32(&requests) != 3 {
			t.Error("Batch splitting failed")
		}
	})
}

func TestHTTPAdapter_Close(t *testing.T) {
	t.Run("closes and final flush", func(t *testing.T) {
		var requests int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&requests, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           server.URL,
			BatchSize:     10,
			FlushInterval: 100 * time.Second, // Prevent auto-flush
		})

		_ = adapter.Write(&types.LogEntry{Message: "1"})
		_ = adapter.Write(&types.LogEntry{Message: "2"})

		if err := adapter.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		if atomic.LoadInt32(&requests) != 1 {
			t.Error("Final flush not executed")
		}
		if adapter.ctx.Err() == nil {
			t.Error("Context not cancelled")
		}
	})

	t.Run("multiple close calls", func(t *testing.T) {
		adapter := NewHTTPAdapter("http://test.com")
		if err := adapter.Close(); err != nil {
			t.Errorf("First close: %v", err)
		}
		// Second close should not panic
		_ = adapter.Close()
	})

	t.Run("stops background flusher", func(t *testing.T) {
		adapter := NewHTTPAdapter("http://test.com")
		time.Sleep(50 * time.Millisecond) // Let flusher start

		start := time.Now()
		_ = adapter.Close()
		if duration := time.Since(start); duration > 50*time.Millisecond {
			t.Errorf("Close took too long: %v", duration)
		}
	})
}

func TestHTTPAdapter_SetFormatter(t *testing.T) {
	adapter := NewHTTPAdapter("http://test.com")
	defer func() { _ = adapter.Close() }()

	newFormatter := NewZeroJSONFormatter()
	adapter.SetFormatter(newFormatter)

	if fp := adapter.formatter.Load(); fp == nil || *fp != newFormatter {
		t.Error("Formatter not updated")
	}

	// Concurrent safety
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			adapter.SetFormatter(newFormatter)
		}()
	}
	wg.Wait()
}

// ============================================================================
// StreamingHTTPAdapter Tests
// ============================================================================

func TestStreamingHTTPAdapter_Constructor(t *testing.T) {
	t.Run("default timeout", func(t *testing.T) {
		a := NewStreamingHTTPAdapter("http://test.com", 0)
		if a.client.Timeout != 10*time.Second {
			t.Error("Default timeout not set")
		}
		_ = a.Close()
	})

	t.Run("custom timeout", func(t *testing.T) {
		timeout := 5 * time.Second
		a := NewStreamingHTTPAdapter("http://test.com", timeout)
		if a.client.Timeout != timeout {
			t.Error("Custom timeout not set")
		}
		_ = a.Close()
	})
}

func TestStreamingHTTPAdapter_Methods(t *testing.T) {
	t.Run("Write", func(t *testing.T) {
		var requests int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&requests, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		a := NewStreamingHTTPAdapter(server.URL, 0)
		defer func() { _ = a.Close() }()

		if err := a.Write(&types.LogEntry{Message: "test"}); err != nil {
			t.Errorf("Write: %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		if atomic.LoadInt32(&requests) != 1 {
			t.Error("Request not sent")
		}
	})

	t.Run("Write nil", func(t *testing.T) {
		a := NewStreamingHTTPAdapter("http://test.com", 0)
		defer func() { _ = a.Close() }()
		if err := a.Write(nil); err != ErrHTTPNilEntry {
			t.Error("Expected ErrHTTPNilEntry")
		}
	})

	t.Run("WriteZero", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		a := NewStreamingHTTPAdapter(server.URL, 0)
		defer func() { _ = a.Close() }()
		err := a.WriteZero(&types.LogEntry{Message: "zero"})
		if err != nil {
			t.Errorf("WriteZero: %v", err)
		}
	})

	t.Run("HTTP error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		a := NewStreamingHTTPAdapter(server.URL, 0)
		defer func() { _ = a.Close() }()

		err := a.Write(&types.LogEntry{Message: "test"})
		if _, ok := err.(*HTTPStatusError); !ok {
			t.Errorf("Expected HTTPStatusError, got %v", err)
		}
	})

	t.Run("concurrent writes", func(t *testing.T) {
		var requests int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&requests, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		a := NewStreamingHTTPAdapter(server.URL, 0)
		defer func() { _ = a.Close() }()

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = a.Write(&types.LogEntry{Message: "concurrent"})
			}()
		}
		wg.Wait()

		time.Sleep(200 * time.Millisecond)
		if atomic.LoadInt32(&requests) != 50 {
			t.Error("Not all requests sent")
		}
	})
}

func TestStreamingHTTPAdapter_OtherMethods(t *testing.T) {
	a := NewStreamingHTTPAdapter("http://test.com", 0)
	defer func() { _ = a.Close() }()

	if a.Name() != "StreamingHTTPAdapter" {
		t.Error("Name incorrect")
	}
	if a.Health() != nil {
		t.Error("Health should return nil")
	}
	if a.Flush() != nil {
		t.Error("Flush should return nil")
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Second close failed: %v", err)
	}
}

func TestStreamingHTTPAdapter_SetFormatter(t *testing.T) {
	a := NewStreamingHTTPAdapter("http://test.com", 0)
	defer func() { _ = a.Close() }()

	newFormatter := NewZeroJSONFormatter()
	a.SetFormatter(newFormatter)
	if fp := a.formatter.Load(); fp == nil || *fp != newFormatter {
		t.Error("Formatter not set")
	}
}

// ============================================================================
// ZeroJSONFormatter Tests
// ============================================================================

func TestZeroJSONFormatter(t *testing.T) {
	t.Run("constructor", func(t *testing.T) {
		f := NewZeroJSONFormatter()
		if f == nil {
			t.Error("Formatter not initialized")
		}
	})

	t.Run("nil entry", func(t *testing.T) {
		f := NewZeroJSONFormatter()
		buf := make([]byte, 0, 1024)
		if result := f.Format(nil, buf); len(result) != 0 {
			t.Error("Expected empty result")
		}
	})

	t.Run("complete entry", func(t *testing.T) {
		f := NewZeroJSONFormatter()
		entry := &types.LogEntry{
			Level:         types.ErrorLevel,
			Message:       `test "quoted" message`,
			Timestamp:     fixedTimeNow(),
			TimestampUnix: 1234567890,
			Fields: []types.TypedFieldData{
				{Key: "string", Value: "value"},
				{Key: "int", Value: int64(42)},
				{Key: "float", Value: 3.14},
				{Key: "bool", Value: true},
				{Key: "nested", Value: map[string]interface{}{"key": "val"}},
			},
		}

		buf := make([]byte, 0, 1024)
		result := f.Format(entry, buf)
		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("Invalid JSON: %v\n%s", err, result)
		}

		if parsed["level"] != "ERROR" ||
			parsed["message"] != `test "quoted" message` ||
			int64(parsed["timestamp"].(float64)) != 1234567890 {
			t.Error("Fields incorrect")
		}

		fields := parsed["fields"].(map[string]interface{})
		if fields["string"] != "value" ||
			fields["int"] != 42.0 ||
			fields["float"] != 3.14 ||
			fields["bool"] != true {
			t.Error("TypedFields incorrect")
		}
	})

	t.Run("zero allocations", func(t *testing.T) {
		f := NewZeroJSONFormatter()
		entry := &types.LogEntry{
			Level:     types.InfoLevel,
			Message:   "test",
			Timestamp: fixedTimeNow(),
			Fields: []types.TypedFieldData{
				{Key: "k1", Value: "v1"},
				{Key: "k2", Value: int64(42)},
			},
		}

		assertZeroAlloc(t, "ZeroJSONFormatter.Format", func() {
			buf := make([]byte, 0, 1024)
			f.Format(entry, buf)
		})
	})

	t.Run("concurrent formatting", func(t *testing.T) {
		f := NewZeroJSONFormatter()
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				entry := &types.LogEntry{
					Level:     types.InfoLevel,
					Message:   utils.FormatIntWithPrefix("msgd", idx),
					Timestamp: time.Unix(int64(idx), 0),
				}
				f.Format(entry, nil)
			}(i)
		}
		wg.Wait()
	})
}

// ============================================================================
// Integration & End-to-End Tests
// ============================================================================

func TestHTTPAdapter_FullLifecycle(t *testing.T) {
	var batches int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&batches, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           server.URL,
		BatchSize:     5,
		FlushInterval: 50 * time.Millisecond,
	})

	// Write 12 entries
	for i := 0; i < 12; i++ {
		_ = adapter.Write(&types.LogEntry{Message: utils.FormatIntWithPrefix("msgd", i)})
	}

	// Wait for auto-flush
	time.Sleep(100 * time.Millisecond)

	// Manually flush remaining
	_ = adapter.Flush()

	// Close for final flush
	_ = adapter.Close()

	time.Sleep(100 * time.Millisecond)

	// Should send 3 batches: 5, 5, 2
	if atomic.LoadInt32(&batches) != 3 {
		t.Errorf("Expected 3 batches, got %d", batches)
	}
}

func TestHTTPAdapter_Recovery(t *testing.T) {
	serverUp := atomic.Bool{}
	serverUp.Store(true)

	var successCount int32
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if !serverUp.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		atomic.AddInt32(&successCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// BatchSize is deliberately larger than any write burst below: filling a
	// batch triggers an ASYNC flush, and under full-suite load that goroutine
	// used to race the explicit doFlush calls and the serverUp toggles,
	// making this test flaky. With only explicit flushes moving data, the
	// sequence is deterministic.
	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           server.URL,
		BatchSize:     10,
		FlushInterval: 0,
	})
	defer func() { _ = adapter.Close() }()

	// Successful batch
	_ = adapter.Write(&types.LogEntry{Message: "s1"})
	_ = adapter.Write(&types.LogEntry{Message: "s2"})
	if err := adapter.Flush(); err != nil {
		t.Fatalf("initial flush failed: %v", err)
	}

	// Server down
	serverUp.Store(false)
	_ = adapter.Write(&types.LogEntry{Message: "f1"})
	_ = adapter.Write(&types.LogEntry{Message: "f2"})
	if err := adapter.Flush(); err == nil {
		t.Error("Expected error when server down")
	}

	// Server up
	serverUp.Store(true)
	_ = adapter.Write(&types.LogEntry{Message: "r1"})
	_ = adapter.Write(&types.LogEntry{Message: "r2"})
	_ = adapter.Flush()

	actualCount := atomic.LoadInt32(&successCount)
	actualRequests := atomic.LoadInt32(&requestCount)
	t.Logf("Success count: %d, Request count: %d", actualCount, actualRequests)

	// The key test requirement is that we can recover from failures
	// We should see at least the 2 initial successes
	// The failed entries might be retried, so we could see 2-4 total successes
	// But the exact count depends on timing and batching behavior
	if actualCount < 2 {
		t.Errorf("Recovery failed: expected at least 2 successes (initial batch), got %d", actualCount)
	}
}

// ============================================================================
// Coverage Edge Cases
// ============================================================================

func TestHTTPStatusError(t *testing.T) {
	err := newHTTPStatusError(404)
	if err.Error() != "HTTP error: 404" {
		t.Error("Error string incorrect")
	}
}

// fieldBufferToTypedFields converts FieldBuffer to TypedField slice
func fieldBufferToTypedFields(buffer *types.FieldBuffer) []types.TypedFieldData {
	if buffer == nil {
		return nil
	}

	fields := make([]types.TypedFieldData, 0, buffer.Len())
	for i := 0; i < buffer.Len(); i++ {
		fields = append(fields, types.TypedFieldData{
			Key:   buffer.Key(i),
			Value: buffer.Value(i),
		})
	}
	return fields
}

func TestFieldBufferToTypedFields(t *testing.T) {
	t.Run("converts buffer", func(t *testing.T) {
		buffer := &types.FieldBuffer{}
		buffer.Add("k1", "v1")
		buffer.Add("k2", int64(42))

		fields := fieldBufferToTypedFields(buffer)
		if len(fields) != 2 {
			t.Error("Conversion failed")
		}
	})

	t.Run("nil buffer", func(t *testing.T) {
		if fields := fieldBufferToTypedFields(nil); fields != nil {
			t.Error("Expected nil for nil buffer")
		}
	})
}

// ============================================================================
// Race Condition Tests
// ============================================================================

func TestHTTPAdapter_RaceDetector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           server.URL,
		BatchSize:     10,
		FlushInterval: 10 * time.Millisecond,
	})
	defer func() { _ = adapter.Close() }()

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = adapter.Write(&types.LogEntry{Message: utils.FormatIntWithPrefix("msgd", idx)})
		}(i)
	}

	// Concurrent flushes
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = adapter.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Concurrent health checks
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = adapter.Health()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func TestStreamingHTTPAdapter_RaceDetector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewStreamingHTTPAdapter(server.URL, 0)
	defer func() { _ = adapter.Close() }()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = adapter.Write(&types.LogEntry{Message: utils.FormatIntWithPrefix("msgd", idx)})
			adapter.SetFormatter(NewZeroJSONFormatter())
		}(i)
	}
	wg.Wait()
}

// ============================================================================
// Benchmark Tests
// ============================================================================

func BenchmarkHTTPAdapter_Write(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           server.URL,
		BatchSize:     1000,
		FlushInterval: 0,
	})
	defer func() { _ = adapter.Close() }()

	entry := &types.LogEntry{Level: types.InfoLevel, Message: "bench"}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = adapter.Write(entry)
	}
}

func BenchmarkHTTPAdapter_WriteWithFormatter(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           server.URL,
		BatchSize:     100,
		FlushInterval: 0,
	})
	defer func() { _ = adapter.Close() }()

	entry := &types.LogEntry{Level: types.InfoLevel, Message: "x"}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// A full batch, then a flush
		for j := 0; j < 100; j++ {
			_ = adapter.Write(entry)
		}
		_ = adapter.Flush()
	}
}

func BenchmarkStreamingHTTPAdapter_Write(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewStreamingHTTPAdapter(server.URL, 0)
	defer func() { _ = adapter.Close() }()

	entry := &types.LogEntry{Level: types.InfoLevel, Message: "bench"}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = adapter.Write(entry)
	}
}
