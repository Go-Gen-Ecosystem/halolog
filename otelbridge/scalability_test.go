package otelbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/core"
	"github.com/go-gen-ecosystem/halolog/types"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

type countingExporter struct{ count atomic.Int64 }

func (e *countingExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.count.Add(int64(len(records)))
	return nil
}
func (*countingExporter) Shutdown(context.Context) error   { return nil }
func (*countingExporter) ForceFlush(context.Context) error { return nil }

// This is a closed-loop diagnostic, not a production latency SLO. Every call
// includes two clock reads for instrumentation; there is no network or collector.
// Fixed total work allows comparing concurrency without multiplying load size.
// Request/entry setup, sample allocation and sorting are outside the timed region.
// Writing each duration sample still contributes to the aggregate elapsed time.
func TestPipelineScale(t *testing.T) {
	const total = 262144
	for _, pipeline := range []string{"adapter_api", "adapter_sdk", "bind_core_sdk", "bind_core_json_sdk"} {
		for _, workers := range []int{1, 4, 16, 64} {
			t.Run(fmt.Sprintf("%s/workers=%d", pipeline, workers), func(t *testing.T) {
				exp := &countingExporter{}
				var provider *sdklog.LoggerProvider
				var adapter *Adapter
				if pipeline == "adapter_api" {
					adapter = benchAdapter()
				} else {
					provider = sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
					adapter = NewAdapter("review/scale", WithLoggerProvider(provider))
					t.Cleanup(func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := provider.Shutdown(ctx); err != nil {
							t.Error(err)
						}
					})
				}
				var base *core.Logger
				if pipeline == "bind_core_sdk" || pipeline == "bind_core_json_sdk" {
					adapters := []types.Adapter{adapter}
					if pipeline == "bind_core_json_sdk" {
						adapters = append(adapters, console.NewWithWriter(io.Discard, jsonfmt.NewJsonFormatter()))
					}
					base = core.NewLogger(core.Config{Level: types.InfoLevel, Adapters: adapters})
				}
				emitters := make([]func(int) error, workers)
				for worker := range workers {
					ctx, e := requestFixture(uint64(worker + 1))
					if base == nil {
						emitters[worker] = func(seq int) error { e.Fields[0].Val = types.IntValue(seq); return adapter.Write(e) }
					} else {
						child := Bind(ctx, base)
						emitters[worker] = func(seq int) error {
							child.Typed().WithInt("field0", seq).WithInt("field1", 1).Info("measured")
							return nil
						}
					}
				}
				samples := make([]time.Duration, total)
				start := make(chan struct{})
				var wg sync.WaitGroup
				var failures atomic.Int64
				for worker := range workers {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						for i := worker; i < total; i += workers {
							before := time.Now()
							if err := emitters[worker](i); err != nil {
								failures.Add(1)
							}
							samples[i] = time.Since(before)
						}
					}()
				}
				before := time.Now()
				close(start)
				wg.Wait()
				elapsed := time.Since(before)
				if failures.Load() != 0 {
					t.Fatalf("write failures: %d", failures.Load())
				}
				if provider != nil && exp.count.Load() != total {
					t.Fatalf("exported %d/%d records", exp.count.Load(), total)
				}
				slices.Sort(samples)
				zeros := 0
				for _, sample := range samples {
					if sample == 0 {
						zeros++
					}
				}
				if zeros > 0 {
					t.Logf("pipeline=%s records=%d workers=%d records/s=%.0f exported=%d zero_duration_samples=%d; latency percentiles withheld: insufficient clock resolution", pipeline, total, workers, float64(total)/elapsed.Seconds(), exp.count.Load(), zeros)
				} else {
					t.Logf("pipeline=%s records=%d workers=%d records/s=%.0f p50=%s p95=%s p99=%s exported=%d", pipeline, total, workers, float64(total)/elapsed.Seconds(), samples[total/2], samples[total*95/100], samples[total*99/100], exp.count.Load())
				}
			})
		}
	}
}

type gatedExporter struct {
	countingExporter
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	recovered atomic.Bool
}

func (e *gatedExporter) Export(ctx context.Context, records []sdklog.Record) error {
	e.once.Do(func() { close(e.entered) })
	select {
	case <-e.release:
		for _, record := range records {
			if record.Body().AsString() == "recovery-sentinel" {
				e.recovered.Store(true)
			}
		}
		return e.countingExporter.Export(ctx, records)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Saturation is expected to drop records: the SDK queue is bounded. This test
// checks that adapter calls complete, flush observes its deadline, then recovery
// drains retained records. It does not promise lossless logging under overload.
func TestBatchBackpressureDeadlineAndRecovery(t *testing.T) {
	exp := &gatedExporter{entered: make(chan struct{}), release: make(chan struct{})}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp,
		sdklog.WithMaxQueueSize(4), sdklog.WithExportMaxBatchSize(1), sdklog.WithExportBufferSize(1),
		sdklog.WithExportInterval(time.Millisecond), sdklog.WithExportTimeout(5*time.Second))))
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(exp.release) }) }
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	a := NewAdapter("review/pressure", WithLoggerProvider(provider), WithFlushTimeout(20*time.Millisecond))
	_, e := requestFixture(1)
	if err := a.Write(e); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exp.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exporter did not start")
	}
	done := make(chan struct{})
	var writeFailures atomic.Int64
	go func() {
		defer close(done)
		for range 1024 {
			if err := a.Write(e); err != nil {
				writeFailures.Add(1)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded SDK queue blocked adapter writes")
	}
	if writeFailures.Load() != 0 {
		t.Fatalf("adapter rejected %d writes; cannot attribute loss solely to SDK queue", writeFailures.Load())
	}
	if err := a.Flush(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked exporter flush: got %v, want context.DeadlineExceeded", err)
	}
	release()
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.ForceFlush(flushCtx); err != nil {
		t.Fatal(err)
	}
	exported := exp.count.Load()
	if exported == 0 || exported >= 1025 {
		t.Fatalf("expected bounded retention and drops, exported %d/1025", exported)
	}
	e.Message = "recovery-sentinel"
	if err := a.Write(e); err != nil {
		t.Fatal(err)
	}
	if err := provider.ForceFlush(flushCtx); err != nil {
		t.Fatal(err)
	}
	if !exp.recovered.Load() {
		t.Fatal("new traffic was not exported after recovery")
	}
	t.Logf("submitted=1025 exported=%d dropped=%d; deadline surfaced, queue drained, new sentinel exported", exported, 1025-exported)
}
