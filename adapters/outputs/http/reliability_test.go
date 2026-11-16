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

package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// collector is an endpoint that rejects invalid JSON with 400, as real
// collectors do, and records the message of every entry it accepts.
type collector struct {
	*httptest.Server
	mu       sync.Mutex
	lines    map[string]int
	requests atomic.Int64
	status   atomic.Int32 // when non-zero, every request is answered with it
	delay    atomic.Int64 // nanoseconds each request waits before it commits
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{lines: map[string]int{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		var batch struct {
			Count   int `json:"count"`
			Entries []struct {
				Message string `json:"message"`
			} `json:"entries"`
		}
		if json.Unmarshal(body, &batch) != nil || batch.Count != len(batch.Entries) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if status := c.status.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		select {
		case <-time.After(time.Duration(c.delay.Load())):
		case <-r.Context().Done():
			return // the client gave up: nothing committed
		}
		c.mu.Lock()
		for _, e := range batch.Entries {
			c.lines[e.Message]++
		}
		c.mu.Unlock()
	}))
	t.Cleanup(c.Close)
	return c
}

// delivered returns the number of distinct lines accepted and fails the test
// if any line was accepted twice.
func (c *collector) delivered(t *testing.T) int {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for line, n := range c.lines {
		if n != 1 {
			t.Errorf("line %q delivered %d times", line, n)
		}
	}
	return len(c.lines)
}

// hangingServer accepts requests and never answers them.
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

func line(i int) *types.LogEntry {
	return &types.LogEntry{Level: types.InfoLevel, Message: fmt.Sprintf("line-%d", i), Timestamp: time.Now()}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// A failed batch stays queued, with every line behind it, until the endpoint
// recovers; no line is lost or delivered twice.
func TestHTTPAdapter_FailedBatchKeepsEveryLine(t *testing.T) {
	c := newCollector(t)
	c.status.Store(http.StatusInternalServerError)
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, BatchSize: 10, FlushInterval: time.Hour})

	for i := range 40 {
		if err := a.Write(line(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := a.Flush(); err == nil {
		t.Fatal("Flush against a failing endpoint returned nil")
	}

	c.status.Store(0)
	if err := a.Close(); err != nil {
		t.Fatalf("Close after the endpoint recovered: %v", err)
	}
	if got := c.delivered(t); got != 40 {
		t.Fatalf("delivered %d of 40 lines", got)
	}
}

// Messages and field keys and values are escaped, so no line can make the
// payload invalid JSON.
func TestHTTPAdapter_PayloadStaysValidJSON(t *testing.T) {
	c := newCollector(t)
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, FlushInterval: time.Hour})
	defer func() { _ = a.Close() }()

	messages := []string{`C:\data\x.log`, `say "hi"`, "two\nlines", "tab\tand\rcr", "nul\x00byte", "bad utf8 \xff", "Unicode ✓"}
	for _, msg := range messages {
		entry := &types.LogEntry{Level: types.WarnLevel, Message: msg, Fields: []types.TypedFieldData{
			{Key: `k"ey\`, Value: "v\\al\n"},
			{Key: "typed", Val: types.StringValue("\x01")},
		}}
		if err := a.Write(entry); err != nil {
			t.Fatalf("Write(%q): %v", msg, err)
		}
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v (the collector rejects invalid JSON with 400)", err)
	}
	if got := c.delivered(t); got != len(messages) {
		t.Fatalf("delivered %d of %d lines", got, len(messages))
	}
}

// A batch the endpoint rejects for good (a 4xx other than 408 or 429) is
// dropped, counted, and reported by Close; delivery goes on behind it.
func TestHTTPAdapter_RejectedBatchIsDropped(t *testing.T) {
	c := newCollector(t)
	c.status.Store(http.StatusForbidden)
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, FlushInterval: time.Hour})

	for i := range 3 {
		_ = a.Write(line(i))
	}
	var status *HTTPStatusError
	if err := a.Flush(); !errors.As(err, &status) || status.StatusCode != http.StatusForbidden {
		t.Fatalf("Flush = %v, want the 403", err)
	}
	if err := a.Flush(); err != nil || c.requests.Load() != 1 {
		t.Fatalf("the rejected batch was retried: Flush = %v after %d requests", err, c.requests.Load())
	}

	c.status.Store(0)
	_ = a.Write(line(3))
	err := a.Close()
	if !errors.Is(err, ErrHTTPUndelivered) || !strings.Contains(err.Error(), "3 lines") {
		t.Fatalf("Close = %v, want ErrHTTPUndelivered for 3 lines", err)
	}
	if got := c.delivered(t); got != 1 {
		t.Fatalf("delivered %d lines after the rejected batch, want 1", got)
	}
}

// Close waits for a batch already on the wire instead of cancelling it.
func TestHTTPAdapter_CloseWaitsForInFlightBatch(t *testing.T) {
	c := newCollector(t)
	c.delay.Store(int64(200 * time.Millisecond))
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, BatchSize: 10})

	for i := range 10 {
		_ = a.Write(line(i))
	}
	waitUntil(t, "the batch to be sent", func() bool { return c.requests.Load() > 0 })
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := c.delivered(t); got != 10 {
		t.Fatalf("delivered %d of 10 lines", got)
	}
}

// Against an endpoint that never answers, Close gives up after the request
// timeout and reports the lines it could not deliver.
func TestHTTPAdapter_CloseReportsUndeliveredLines(t *testing.T) {
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: hangingServer(t).URL, Timeout: 200 * time.Millisecond})
	for i := range 5 {
		_ = a.Write(line(i))
	}

	start := time.Now()
	err := a.Close()
	if !errors.Is(err, ErrHTTPUndelivered) || !strings.Contains(err.Error(), "5 lines") {
		t.Fatalf("Close = %v, want ErrHTTPUndelivered for 5 lines", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Close took %v against a 200ms timeout", took)
	}
}

// After Close, Write and Flush report ErrHTTPClosed instead of losing lines,
// and Close can be called again.
func TestHTTPAdapter_ClosedAdapterRefusesLines(t *testing.T) {
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: newCollector(t).URL})
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.Write(line(0)); !errors.Is(err, ErrHTTPClosed) {
		t.Fatalf("Write after Close = %v, want ErrHTTPClosed", err)
	}
	if err := a.Flush(); !errors.Is(err, ErrHTTPClosed) {
		t.Fatalf("Flush after Close = %v, want ErrHTTPClosed", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// In streaming mode no Write returns nil for a line that was not delivered,
// and lines that failed are delivered, in full, once the endpoint recovers.
func TestStreamingHTTPAdapter_NeverAcknowledgesAFailedLine(t *testing.T) {
	c := newCollector(t)
	c.status.Store(http.StatusServiceUnavailable)
	a := NewStreamingHTTPAdapter(c.URL, 2*time.Second)
	defer func() { _ = a.Close() }()

	var acked atomic.Int64
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				if a.Write(line(g*100+i)) == nil {
					acked.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if n := acked.Load(); n != 0 {
		t.Fatalf("%d of 200 Writes returned nil while every request failed", n)
	}

	c.status.Store(0)
	if err := a.Write(line(1000)); err != nil {
		t.Fatalf("Write after the endpoint recovered: %v", err)
	}
	if got := c.delivered(t); got != 201 {
		t.Fatalf("delivered %d of 201 lines after the endpoint recovered", got)
	}
}

// Zero and negative settings take their defaults rather than panicking,
// crashing the delivery goroutine, or disabling the timeout.
func TestHTTPAdapter_InvalidSettingsTakeDefaults(t *testing.T) {
	for _, n := range []int{0, -1} {
		a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
			URL:           "http://127.0.0.1:1",
			BatchSize:     n,
			FlushInterval: time.Duration(n) * time.Second,
			Timeout:       time.Duration(n) * time.Second,
		})
		if a.batchSize != defaultBatchSize || a.flushInterval != defaultFlushInterval || a.client.Timeout != defaultTimeout {
			t.Errorf("settings %d: batch %d, interval %v, timeout %v", n, a.batchSize, a.flushInterval, a.client.Timeout)
		}
		_ = a.Close()
	}
}

// While the endpoint hangs, the buffer stays bounded: Write drops the line
// with ErrHTTPBufferFull and counts it.
func TestHTTPAdapter_BufferIsBoundedWhileEndpointHangs(t *testing.T) {
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: hangingServer(t).URL, BatchSize: 10, Timeout: 200 * time.Millisecond})

	var full int
	for i := range 5000 {
		if errors.Is(a.Write(line(i)), ErrHTTPBufferFull) {
			full++
		}
	}
	if full == 0 || a.unsent.Load() > minBuffered {
		t.Fatalf("%d Writes reported a full buffer, %d lines held (bound %d)", full, a.unsent.Load(), minBuffered)
	}
	if err := a.Close(); !errors.Is(err, ErrHTTPUndelivered) {
		t.Fatalf("Close = %v, want ErrHTTPUndelivered", err)
	}
}

// Health reports a failing endpoint and clears once a delivery succeeds.
func TestHTTPAdapter_HealthFollowsDeliveries(t *testing.T) {
	c := newCollector(t)
	c.status.Store(http.StatusServiceUnavailable)
	a := NewStreamingHTTPAdapter(c.URL, time.Second)
	defer func() { _ = a.Close() }()

	_ = a.Write(line(0))
	if a.Health() == nil {
		t.Fatal("Health = nil while every request fails")
	}
	c.status.Store(0)
	_ = a.Write(line(1))
	if err := a.Health(); err != nil {
		t.Fatalf("Health after a delivery = %v", err)
	}
}

// A failing endpoint is retried with exponential backoff, not once per batch.
func TestHTTPAdapter_BacksOffWhileEndpointFails(t *testing.T) {
	c := newCollector(t)
	c.status.Store(http.StatusServiceUnavailable)
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, BatchSize: 2, FlushInterval: 10 * time.Millisecond})
	defer func() { _ = a.Close() }()

	for i := range 300 { // 150 full batches over about 300ms
		_ = a.Write(line(i))
		time.Sleep(time.Millisecond)
	}
	if n := c.requests.Load(); n > 10 {
		t.Fatalf("%d requests in 300ms against a failing endpoint", n)
	}
}

// Close releases the adapter's idle connections.
func TestHTTPAdapter_CloseReleasesConnections(t *testing.T) {
	var open atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	srv.Start()
	defer srv.Close()

	for i := range 20 {
		a := NewStreamingHTTPAdapter(srv.URL, time.Second)
		_ = a.Write(line(i))
		_ = a.Close()
	}
	waitUntil(t, "closed adapters to release their connections", func() bool { return open.Load() == 0 })
}

// Writers, flushes, formatter swaps, and health checks race a Close.
func TestHTTPAdapter_ConcurrentUseAndClose(t *testing.T) {
	c := newCollector(t)
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, BatchSize: 7, FlushInterval: time.Millisecond})

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				_ = a.Write(line(g*1000 + i))
				if i%50 == 0 {
					_ = a.Flush()
					a.SetFormatter(NewZeroJSONFormatter())
					_ = a.Health()
				}
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	_ = a.Close()
	wg.Wait()
	_ = a.Close()
}

// countingTransport counts the requests that go through it.
type countingTransport struct{ requests atomic.Int64 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.requests.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

// A caller's client carries every request, and Timeout still bounds each one
// even when that client sets none.
func TestHTTPAdapter_UsesTheCallersClient(t *testing.T) {
	transport := &countingTransport{}
	client := &http.Client{Transport: transport} // no timeout of its own

	c := newCollector(t)
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: c.URL, Client: client})
	_ = a.Write(line(0))
	if err := a.Flush(); err != nil || transport.requests.Load() != 1 {
		t.Fatalf("Flush = %v after %d requests through the caller's client", err, transport.requests.Load())
	}
	_ = a.Close()

	a = NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: hangingServer(t).URL, Client: client, Timeout: 200 * time.Millisecond})
	_ = a.Write(line(1))
	start := time.Now()
	if err := a.Flush(); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("Flush against a hanging endpoint = %v after %v, want a timeout near 200ms", err, time.Since(start))
	}
	_ = a.Close()
}
