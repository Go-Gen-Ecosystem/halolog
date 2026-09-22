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

// Package otelbridge — draining the export pipeline.
//
// Logger.Fatal writes its line, calls Logger.Flush, and exits from inside the
// logging call. If the adapter's Flush does not reach the provider, the last
// line a program ever writes dies in a batch queue with no caller left to
// drain it.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/core"
	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// flushCountingProvider records ForceFlush calls without an SDK.
type flushCountingProvider struct {
	embedded.LoggerProvider
	rec     *recorder
	flushes atomic.Int64
	err     error
}

func (p *flushCountingProvider) Logger(name string, opts ...otellog.LoggerOption) otellog.Logger {
	return p.rec.Logger(name, opts...)
}

func (p *flushCountingProvider) ForceFlush(context.Context) error {
	p.flushes.Add(1)
	return p.err
}

// blockingProvider never drains on its own, so Flush returns only when its
// deadline fires — the wedged-exporter case.
type blockingProvider struct {
	embedded.LoggerProvider
	rec *recorder
}

func (p *blockingProvider) Logger(name string, opts ...otellog.LoggerOption) otellog.Logger {
	return p.rec.Logger(name, opts...)
}

func (p *blockingProvider) ForceFlush(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestAdapter_FlushAndCloseDrainTheProvider(t *testing.T) {
	p := &flushCountingProvider{rec: &recorder{}}
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(p))

	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := p.flushes.Load(); got != 1 {
		t.Fatalf("Flush reached the provider %d times, want 1", got)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := p.flushes.Load(); got != 2 {
		t.Fatalf("Close must drain too; provider saw %d flushes, want 2", got)
	}
}

// A provider with no ForceFlush is not an error; there is simply nothing to
// drain.
func TestAdapter_FlushIsQuietWithoutForceFlush(t *testing.T) {
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(&recorder{}))
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAdapter_FlushReportsProviderFailure(t *testing.T) {
	p := &flushCountingProvider{rec: &recorder{}, err: context.DeadlineExceeded}
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(p))

	if err := a.Flush(); err == nil {
		t.Fatal("a failing ForceFlush must surface through Flush")
	}
}

// newFatalLogger wires a Fatal-capable logger over the given adapter, with an
// exit hook that runs assertions at the moment the process would die.
func newFatalLogger(t *testing.T, a types.Adapter, onExit func()) *core.Logger {
	t.Helper()
	var buf bytes.Buffer
	return core.NewLogger(core.Config{
		Level:    types.InfoLevel,
		ExitFunc: func(int) { onExit() },
		Adapters: []types.Adapter{
			console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter()),
			a,
		},
	})
}

// The case that matters: a batching processor, a Fatal line, and no caller
// left to drain anything. The assertion is inside the exit hook, because
// "exported eventually" is not the guarantee — "exported before the process
// dies" is.
func TestSDK_FatalRecordIsExportedBeforeExit(t *testing.T) {
	exp := &memExporter{}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("provider shutdown: %v", err)
		}
	})

	var checked atomic.Bool
	logger := newFatalLogger(t,
		NewAdapter("github.com/acme/checkout", WithLoggerProvider(provider)),
		func() {
			checked.Store(true)
			exp.mu.Lock()
			defer exp.mu.Unlock()
			if len(exp.records) != 1 {
				t.Errorf("at exit the exporter holds %d records, want 1", len(exp.records))
				return
			}
			rec := exp.records[0]
			if got := rec.Body().AsString(); got != "the last line this program writes" {
				t.Errorf("exported body = %q", got)
			}
			if got := rec.Severity(); got != otellog.SeverityFatal1 {
				t.Errorf("exported severity = %v, want %v", got, otellog.SeverityFatal1)
			}
		},
	)

	logger.Fatal("the last line this program writes")

	if !checked.Load() {
		t.Fatal("Fatal never reached the exit hook")
	}
}

// A provider that fails to drain must not stop the program terminating.
func TestSDK_FatalStillExitsWhenTheFlushFails(t *testing.T) {
	p := &flushCountingProvider{rec: &recorder{}, err: errors.New("exporter refused")}
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(p))

	if err := a.Flush(); err == nil {
		t.Fatal("a failing ForceFlush must surface through Flush")
	}

	var exited atomic.Bool
	newFatalLogger(t, a, func() { exited.Store(true) }).Fatal("boom")

	if !exited.Load() {
		t.Fatal("a failed drain must not stop Fatal reaching the exit hook")
	}
	if p.flushes.Load() == 0 {
		t.Fatal("Fatal did not attempt to drain the provider")
	}
}

// A wedged exporter must cost at most the flush timeout, not the process.
func TestSDK_FatalExitsWhenTheFlushTimesOut(t *testing.T) {
	a := NewAdapter("halolog/otelbridge_test",
		WithLoggerProvider(&blockingProvider{rec: &recorder{}}),
		WithFlushTimeout(50*time.Millisecond),
	)

	start := time.Now()
	if err := a.Flush(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Flush error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Flush waited %v; the deadline did not bound it", elapsed)
	}

	var exited atomic.Bool
	start = time.Now()
	newFatalLogger(t, a, func() { exited.Store(true) }).Fatal("boom")
	elapsed := time.Since(start)

	if !exited.Load() {
		t.Fatal("a wedged exporter must not stop Fatal reaching the exit hook")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Fatal took %v to reach the exit hook; the flush was effectively unbounded", elapsed)
	}
}

// A non-positive timeout is the documented opt-in to waiting indefinitely.
func TestAdapter_NonPositiveFlushTimeoutWaits(t *testing.T) {
	p := &flushCountingProvider{rec: &recorder{}}
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(p), WithFlushTimeout(0))

	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if p.flushes.Load() != 1 {
		t.Fatalf("provider saw %d flushes, want 1", p.flushes.Load())
	}
}
