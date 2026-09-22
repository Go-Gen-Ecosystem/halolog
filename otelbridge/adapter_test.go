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

// Package otelbridge — log-record emission tests.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/core"
	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	"go.opentelemetry.io/otel/trace"
)

const (
	testTraceHex = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanHex  = "00f067aa0ba902b7"
)

// emitted is one captured Emit call: the record plus the span context it
// carried, which is the half that proves correlation survived the bridge.
type emitted struct {
	record otellog.Record
	span   trace.SpanContext
}

// The Record accessors take a pointer receiver, so these helpers exist to read
// a record straight out of a returned value without a local variable first.
func (e emitted) timestamp() time.Time { return e.record.Timestamp() }
func (e emitted) body() string         { return e.record.Body().AsString() }

func (e emitted) attrs() map[string]otellog.Value {
	out := make(map[string]otellog.Value)
	e.record.WalkAttributes(func(kv otellog.KeyValue) bool {
		out[kv.Key] = kv.Value
		return true
	})
	return out
}

// recorder is a minimal LoggerProvider capturing what the adapter emits, so
// the tests need no SDK and no test-only module dependency.
type recorder struct {
	embedded.LoggerProvider

	mu         sync.Mutex
	records    []emitted
	scopeName  string
	disableAll bool
}

func (r *recorder) Logger(name string, _ ...otellog.LoggerOption) otellog.Logger {
	r.scopeName = name
	return recLogger{r: r}
}

// recLogger is separate from recorder only because embedding embedded.Logger
// would collide with the provider's own Logger method.
type recLogger struct {
	embedded.Logger
	r *recorder
}

func (l recLogger) Emit(ctx context.Context, rec otellog.Record) {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	l.r.records = append(l.r.records, emitted{record: rec, span: trace.SpanContextFromContext(ctx)})
}

func (l recLogger) Enabled(context.Context, otellog.EnabledParameters) bool {
	return !l.r.disableAll
}

func (r *recorder) all() []emitted {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]emitted(nil), r.records...)
}

func (r *recorder) only(t *testing.T) emitted {
	t.Helper()
	got := r.all()
	if len(got) != 1 {
		t.Fatalf("want exactly 1 emitted record, got %d", len(got))
	}
	return got[0]
}

// newFanoutLogger wires the pair this adapter exists for: a console adapter
// still writing JSON to buf, and the OTel adapter emitting into rec.
func newFanoutLogger(buf *bytes.Buffer, rec *recorder) *core.Logger {
	return core.NewLogger(core.Config{
		Level: types.InfoLevel,
		Adapters: []types.Adapter{
			console.NewWithWriter(buf, jsonfmt.NewJsonFormatter()),
			NewAdapter("halolog/otelbridge_test", WithLoggerProvider(rec)),
		},
	})
}

func sampledContext() context.Context {
	traceID, _ := trace.TraceIDFromHex(testTraceHex)
	spanID, _ := trace.SpanIDFromHex(testSpanHex)
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))
}

func TestAdapter_FansOutToConsoleAndOTel(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	log := newFanoutLogger(&buf, rec)

	log.Typed().WithString("tenant", "acme").Info("handled")

	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &m); err != nil {
		t.Fatalf("console output is not valid JSON: %v (%q)", err, buf.String())
	}
	if m["message"] != "handled" {
		t.Fatalf("console line lost the message: %v", m)
	}

	got := rec.only(t)
	if body := got.record.Body().AsString(); body != "handled" {
		t.Fatalf("record body = %q, want %q", body, "handled")
	}
	if v, ok := got.attrs()["tenant"]; !ok || v.AsString() != "acme" {
		t.Fatalf("record lost the tenant attribute: %v", got.attrs())
	}
}

func TestAdapter_BoundTraceBecomesRecordSpanContext(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	log := Bind(sampledContext(), newFanoutLogger(&buf, rec))

	log.Info("correlated")

	got := rec.only(t)
	if !got.span.IsValid() {
		t.Fatal("record carried no span context; Honeycomb cannot correlate it")
	}
	if id := got.span.TraceID().String(); id != testTraceHex {
		t.Fatalf("record TraceID = %s, want %s", id, testTraceHex)
	}
	if id := got.span.SpanID().String(); id != testSpanHex {
		t.Fatalf("record SpanID = %s, want %s", id, testSpanHex)
	}
	if !got.span.TraceFlags().IsSampled() {
		t.Fatal("record lost the sampled flag")
	}

	// The correlation fields are the record's identity now, not attributes.
	for _, key := range []string{"trace_id", "span_id", "trace_flags"} {
		if _, dup := got.attrs()[key]; dup {
			t.Fatalf("%s emitted as an attribute as well as on the record: %v", key, got.attrs())
		}
	}
}

func TestAdapter_UncorrelatedEntryEmitsWithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	newFanoutLogger(&buf, rec).Info("no span here")

	got := rec.only(t)
	if got.span.IsValid() {
		t.Fatalf("record invented a span context: %v", got.span)
	}
	if body := got.record.Body().AsString(); body != "no span here" {
		t.Fatalf("record body = %q", body)
	}
}

func TestAdapter_AttributesCoverFieldsComponentAndError(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	log := core.NewLogger(core.Config{
		Level:     types.InfoLevel,
		Component: "checkout",
		Adapters: []types.Adapter{
			console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter()),
			NewAdapter("halolog/otelbridge_test", WithLoggerProvider(rec)),
		},
	})

	log.WithError(errors.New("card declined")).
		WithField("attempt", 3).
		Error("payment failed")

	attrs := rec.only(t).attrs()
	if v, ok := attrs["component"]; !ok || v.AsString() != "checkout" {
		t.Fatalf("component missing from attributes: %v", attrs)
	}
	if v, ok := attrs["attempt"]; !ok || v.AsInt64() != 3 {
		t.Fatalf("attempt missing or wrong: %v", attrs)
	}
	if v, ok := attrs["error"]; !ok || !strings.Contains(v.AsString(), "card declined") {
		t.Fatalf("error missing from attributes: %v", attrs)
	}
}

func TestAdapter_SeverityMapping(t *testing.T) {
	cases := []struct {
		level types.LogLevel
		want  otellog.Severity
		text  string
	}{
		{types.TraceLevel, otellog.SeverityTrace1, "TRACE"},
		{types.DebugLevel, otellog.SeverityDebug1, "DEBUG"},
		{types.InfoLevel, otellog.SeverityInfo1, "INFO"},
		{types.WarnLevel, otellog.SeverityWarn1, "WARN"},
		{types.ErrorLevel, otellog.SeverityError1, "ERROR"},
		{types.FatalLevel, otellog.SeverityFatal1, "FATAL"},
		{types.PanicLevel, otellog.SeverityFatal2, "PANIC"},
	}
	for _, tc := range cases {
		if got := severityOf(tc.level); got != tc.want {
			t.Errorf("severityOf(%s) = %v, want %v", tc.text, got, tc.want)
		}
		if got := tc.level.String(); got != tc.text {
			t.Errorf("level text = %q, want %q", got, tc.text)
		}
	}
}

func TestAdapter_HonoursEnabled(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{disableAll: true}
	newFanoutLogger(&buf, rec).Info("dropped by the SDK")

	if got := rec.all(); len(got) != 0 {
		t.Fatalf("Enabled=false must suppress the record, got %d", len(got))
	}
	if buf.Len() == 0 {
		t.Fatal("the console adapter must still have written the line")
	}
}

// Entries are pooled and reused, so a record must own its strings by the time
// Emit returns — otherwise later lines rewrite earlier ones.
func TestAdapter_RecordsSurviveEntryRecycling(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	log := Bind(sampledContext(), newFanoutLogger(&buf, rec))

	const lines = 64
	for i := range lines {
		log.Typed().WithInt("seq", i).WithString("msg", "line").Info("recycled")
	}

	got := rec.all()
	if len(got) != lines {
		t.Fatalf("want %d records, got %d", lines, len(got))
	}
	for i, e := range got {
		if body := e.record.Body().AsString(); body != "recycled" {
			t.Fatalf("record %d body was overwritten: %q", i, body)
		}
		if v, ok := e.attrs()["seq"]; !ok || v.AsInt64() != int64(i) {
			t.Fatalf("record %d seq = %v, want %d", i, e.attrs()["seq"], i)
		}
		if e.span.TraceID().String() != testTraceHex {
			t.Fatalf("record %d lost its trace: %v", i, e.span)
		}
	}
}

func TestAdapter_Lifecycle(t *testing.T) {
	rec := &recorder{}
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(rec), WithName("honeycomb"),
		WithVersion("v1.2.3"), WithSchemaURL("https://opentelemetry.io/schemas/1.37.0"))

	if a.Name() != "honeycomb" {
		t.Fatalf("Name() = %q, want %q", a.Name(), "honeycomb")
	}
	if rec.scopeName != "halolog/otelbridge_test" {
		t.Fatalf("scope name = %q", rec.scopeName)
	}
	if err := a.Health(); err != nil {
		t.Fatalf("Health() = %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush() = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	a.SetFormatter(nil) // documented no-op
	if err := a.Write(nil); err != nil {
		t.Fatalf("Write(nil) = %v", err)
	}
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("nil entry must emit nothing, got %d", len(got))
	}

	unnamed := NewAdapter("scope", WithLoggerProvider(rec))
	if unnamed.Name() != DefaultAdapterName {
		t.Fatalf("default name = %q, want %q", unnamed.Name(), DefaultAdapterName)
	}
}
