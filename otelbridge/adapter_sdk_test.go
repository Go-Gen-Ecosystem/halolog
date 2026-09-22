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

// Package otelbridge — end to end through the real Logs SDK.
//
// Every other test here drives a hand-written Logger, which proves what the
// adapter emits but not what an exporter receives. The trace correlation in
// particular only pays off if the SDK lifts the span context off the emitting
// ctx and onto the record, so that claim is worth testing against the SDK
// itself rather than against an assumption about it.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/core"
	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// memExporter collects exported records. Export must not retain the slice, so
// each record is cloned out of it.
type memExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range records {
		e.records = append(e.records, records[i].Clone())
	}
	return nil
}

func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

func (e *memExporter) only(t *testing.T) sdklog.Record {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.records) != 1 {
		t.Fatalf("want exactly 1 exported record, got %d", len(e.records))
	}
	return e.records[0]
}

func recordAttrs(rec sdklog.Record) map[string]otellog.Value {
	out := make(map[string]otellog.Value)
	rec.WalkAttributes(func(kv otellog.KeyValue) bool {
		out[kv.Key] = kv.Value
		return true
	})
	return out
}

// newSDKLogger wires the shipping configuration: console to the buffer, the
// real SDK behind the adapter.
func newSDKLogger(t *testing.T, buf *bytes.Buffer) (*core.Logger, *memExporter) {
	t.Helper()
	exp := &memExporter{}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)),
	)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("provider shutdown: %v", err)
		}
	})

	logger := core.NewLogger(core.Config{
		Level:     types.InfoLevel,
		Component: "checkout",
		Adapters: []types.Adapter{
			console.NewWithWriter(buf, jsonfmt.NewJsonFormatter()),
			NewAdapter("github.com/acme/checkout", WithLoggerProvider(provider), WithVersion("v1.2.3")),
		},
	})
	return logger, exp
}

func TestSDK_CorrelatedRecordReachesTheExporter(t *testing.T) {
	var buf bytes.Buffer
	logger, exp := newSDKLogger(t, &buf)

	Bind(sampledContext(), logger).
		Typed().WithInt("status", 200).WithString("route", "/v1/quotes").
		Warn("handled")

	rec := exp.only(t)

	// The whole reason the adapter re-parses Bind's fields: these are record
	// fields, not attributes, and they are what a backend joins traces on.
	if got := rec.TraceID().String(); got != testTraceHex {
		t.Fatalf("record TraceID = %s, want %s", got, testTraceHex)
	}
	if got := rec.SpanID().String(); got != testSpanHex {
		t.Fatalf("record SpanID = %s, want %s", got, testSpanHex)
	}
	if !rec.TraceFlags().IsSampled() {
		t.Fatal("record lost the sampled flag")
	}

	if got := rec.Body().AsString(); got != "handled" {
		t.Fatalf("record body = %q", got)
	}
	if got := rec.Severity(); got != otellog.SeverityWarn1 {
		t.Fatalf("record severity = %v, want %v", got, otellog.SeverityWarn1)
	}
	if got := rec.SeverityText(); got != "WARN" {
		t.Fatalf("record severity text = %q", got)
	}
	if rec.Timestamp().IsZero() {
		t.Fatal("record reached the exporter unstamped")
	}

	attrs := recordAttrs(rec)
	if got, ok := attrs["status"]; !ok || got.AsInt64() != 200 {
		t.Fatalf("status attribute = %v", attrs["status"])
	}
	if got, ok := attrs["route"]; !ok || got.AsString() != "/v1/quotes" {
		t.Fatalf("route attribute = %v", attrs["route"])
	}
	if got, ok := attrs["component"]; !ok || got.AsString() != "checkout" {
		t.Fatalf("component attribute = %v", attrs["component"])
	}
	for _, key := range []string{"trace_id", "span_id", "trace_flags"} {
		if _, dup := attrs[key]; dup {
			t.Fatalf("%s reached the exporter as an attribute as well: %v", key, attrs)
		}
	}

	// The console adapter is unaffected by any of this.
	if !bytes.Contains(buf.Bytes(), []byte(`"trace_id":"`+testTraceHex+`"`)) {
		t.Fatalf("stderr line lost its correlation field: %s", buf.String())
	}
}

// A trace id with no span id: the SDK copies the trace context out of ctx
// without checking validity, so the record still names its trace.
func TestSDK_TraceIDAloneReachesTheExporter(t *testing.T) {
	var buf bytes.Buffer
	logger, exp := newSDKLogger(t, &buf)

	logger.Typed().WithString("trace_id", testTraceHex).Info("half correlated")

	rec := exp.only(t)
	if got := rec.TraceID().String(); got != testTraceHex {
		t.Fatalf("record TraceID = %s, want %s", got, testTraceHex)
	}
	if rec.SpanID().IsValid() {
		t.Fatalf("record invented a span id: %v", rec.SpanID())
	}
	if _, dup := recordAttrs(rec)["trace_id"]; dup {
		t.Fatal("trace_id was consumed into the record and must not repeat as an attribute")
	}
}

// The SDK's own scope plumbing: the name and version passed to NewAdapter are
// what identify the emitting library downstream.
func TestSDK_InstrumentationScopeIsCarried(t *testing.T) {
	var buf bytes.Buffer
	logger, exp := newSDKLogger(t, &buf)

	logger.Info("scoped")

	rec := exp.only(t)
	scope := rec.InstrumentationScope()
	if scope.Name != "github.com/acme/checkout" {
		t.Fatalf("scope name = %q", scope.Name)
	}
	if scope.Version != "v1.2.3" {
		t.Fatalf("scope version = %q", scope.Version)
	}
}

// A level below the logger's threshold must never reach the exporter.
func TestSDK_FilteredLevelNeverExports(t *testing.T) {
	var buf bytes.Buffer
	logger, exp := newSDKLogger(t, &buf)

	logger.Debug("below threshold")

	exp.mu.Lock()
	defer exp.mu.Unlock()
	if len(exp.records) != 0 {
		t.Fatalf("a filtered line reached the exporter: %d records", len(exp.records))
	}
}

// sampledOnlyProcessor is deliberately context-sensitive. It verifies the
// generic Logger contract: Enabled and OnEmit must see the same reconstructed
// span context. This is not a claim about the SDK's built-in processors.
type sampledOnlyProcessor struct {
	next   sdklog.Processor
	probes atomic.Int64
}

func (p *sampledOnlyProcessor) Enabled(ctx context.Context, params sdklog.EnabledParameters) bool {
	p.probes.Add(1)
	return trace.SpanContextFromContext(ctx).IsSampled() && p.next.Enabled(ctx, params)
}

func (p *sampledOnlyProcessor) OnEmit(ctx context.Context, record *sdklog.Record) error {
	if !trace.SpanContextFromContext(ctx).IsSampled() {
		return nil
	}
	return p.next.OnEmit(ctx, record)
}

func (p *sampledOnlyProcessor) ForceFlush(ctx context.Context) error {
	return p.next.ForceFlush(ctx)
}

func (p *sampledOnlyProcessor) Shutdown(ctx context.Context) error {
	return p.next.Shutdown(ctx)
}

func TestSDK_EnabledUsesTheEmittedSpanContext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags string
		want  int
	}{
		{name: "sampled", flags: "01", want: 1},
		{name: "unsampled", flags: "00", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := &memExporter{}
			gate := &sampledOnlyProcessor{next: sdklog.NewSimpleProcessor(exporter)}
			provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(gate))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})

			entry := correlatedEntry()
			entry.Fields[len(entry.Fields)-1].Val = types.StringValue(tc.flags)
			if err := NewAdapter("test/sampled-gate", WithLoggerProvider(provider)).Write(entry); err != nil {
				t.Fatal(err)
			}
			if gate.probes.Load() != 1 {
				t.Fatalf("Enabled probes = %d, want 1", gate.probes.Load())
			}
			exporter.mu.Lock()
			got := len(exporter.records)
			exporter.mu.Unlock()
			if got != tc.want {
				t.Fatalf("exported = %d, want %d", got, tc.want)
			}
		})
	}
}
