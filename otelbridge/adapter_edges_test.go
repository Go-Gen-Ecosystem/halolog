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

// Package otelbridge — malformed and colliding entries.
//
// The record is an export boundary: an entry the logger would render happily
// must not become a record the backend rejects, and nothing may be dropped on
// the way out just because part of it was unusable.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"errors"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
)

// attrPairs preserves duplicates, which the attrs() map would hide — the whole
// point of several tests here.
func attrPairs(e emitted) []otellog.KeyValue {
	var out []otellog.KeyValue
	e.record.WalkAttributes(func(kv otellog.KeyValue) bool {
		out = append(out, kv)
		return true
	})
	return out
}

func countKey(pairs []otellog.KeyValue, key string) int {
	n := 0
	for _, kv := range pairs {
		if kv.Key == key {
			n++
		}
	}
	return n
}

func entryWithFields(fields ...types.TypedFieldData) *types.LogEntry {
	return &types.LogEntry{Level: types.InfoLevel, Message: "edge", Fields: fields}
}

// The data model lets a record name its trace without naming a span, and the
// SDK copies the trace context out of ctx without checking validity — so a
// trace id still correlates when the span id is missing or malformed. What
// must never happen is a correlation field being dropped from the attributes
// without being consumed into the record.
func TestAdapter_TraceIDAloneStillCorrelates(t *testing.T) {
	cases := []struct {
		name      string
		fields    []types.TypedFieldData
		wantSpan  bool
		keptAttrs []string
	}{
		{
			name:      "span_id missing",
			fields:    []types.TypedFieldData{{Key: keyTraceID.Name, Val: types.StringValue(testTraceHex)}},
			wantSpan:  false,
			keptAttrs: nil,
		},
		{
			name: "span_id malformed stays visible",
			fields: []types.TypedFieldData{
				{Key: keyTraceID.Name, Val: types.StringValue(testTraceHex)},
				{Key: keySpanID.Name, Val: types.StringValue("not-hex")},
			},
			wantSpan:  false,
			keptAttrs: []string{keySpanID.Name},
		},
		{
			name: "trace_flags malformed stays visible",
			fields: []types.TypedFieldData{
				{Key: keyTraceID.Name, Val: types.StringValue(testTraceHex)},
				{Key: keySpanID.Name, Val: types.StringValue(testSpanHex)},
				{Key: keyTraceFlags.Name, Val: types.StringValue("zz")},
			},
			wantSpan:  true,
			keptAttrs: []string{keyTraceFlags.Name},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := emitEntry(t, entryWithFields(tc.fields...))

			if id := got.span.TraceID().String(); id != testTraceHex {
				t.Fatalf("record TraceID = %s, want the trace to correlate on %s", id, testTraceHex)
			}
			if got.span.HasSpanID() != tc.wantSpan {
				t.Fatalf("HasSpanID = %v, want %v", got.span.HasSpanID(), tc.wantSpan)
			}

			attrs := got.attrs()
			if _, dup := attrs[keyTraceID.Name]; dup {
				t.Fatalf("trace_id both on the record and in the attributes: %v", attrs)
			}
			for _, key := range tc.keptAttrs {
				if _, ok := attrs[key]; !ok {
					t.Fatalf("%s did not parse, so it must stay visible in the attributes: %v", key, attrs)
				}
			}
		})
	}
}

// Without a usable trace id there is nothing to correlate on, so everything
// stays where the backend can still see it.
func TestAdapter_UnusableTraceContextKeepsEveryField(t *testing.T) {
	cases := []struct {
		name   string
		fields []types.TypedFieldData
	}{
		{
			name: "trace_id malformed",
			fields: []types.TypedFieldData{
				{Key: keyTraceID.Name, Val: types.StringValue("not-hex")},
				{Key: keySpanID.Name, Val: types.StringValue(testSpanHex)},
			},
		},
		{
			name: "all-zero trace id is not a trace id",
			fields: []types.TypedFieldData{
				{Key: keyTraceID.Name, Val: types.StringValue("00000000000000000000000000000000")},
				{Key: keySpanID.Name, Val: types.StringValue(testSpanHex)},
			},
		},
		{
			name:   "span_id with no trace_id",
			fields: []types.TypedFieldData{{Key: keySpanID.Name, Val: types.StringValue(testSpanHex)}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := emitEntry(t, entryWithFields(tc.fields...))
			if got.span.HasTraceID() {
				t.Fatalf("record claims a trace it cannot have: %v", got.span)
			}
			attrs := got.attrs()
			for _, f := range tc.fields {
				if _, ok := attrs[f.Key]; !ok {
					t.Fatalf("%s was dropped without being consumed: %v", f.Key, attrs)
				}
			}
		})
	}
}

// One key, two values is undefined in the data model. A logged field is the
// more specific value, so it wins and the derived one is suppressed.
func TestAdapter_LoggedFieldWinsOverDerivedKey(t *testing.T) {
	entry := entryWithFields(
		types.TypedFieldData{Key: attrComponent, Val: types.StringValue("from field")},
		types.TypedFieldData{Key: attrError, Val: types.StringValue("from field")},
		types.TypedFieldData{Key: attrFilePath, Val: types.StringValue("from field")},
		types.TypedFieldData{Key: attrLineNo, Val: types.IntValue(1)},
	)
	entry.Component = "from entry"
	entry.File = "from_entry.go"
	entry.Line = 99
	entry.SetError(errors.New("from entry"))

	pairs := attrPairs(emitEntry(t, entry))
	for _, key := range []string{attrComponent, attrError, attrFilePath, attrLineNo} {
		if n := countKey(pairs, key); n != 1 {
			t.Fatalf("%s appears %d times, want exactly 1: %v", key, n, pairs)
		}
	}
	for _, kv := range pairs {
		if kv.Key == attrLineNo {
			if kv.Value.AsInt64() != 1 {
				t.Fatalf("%s = %v, want the logged field's value", kv.Key, kv.Value)
			}
			continue
		}
		if kv.Value.AsString() != "from field" {
			t.Fatalf("%s = %v, want the logged field's value", kv.Key, kv.Value)
		}
	}
}

// Without a competing field the metadata still comes through.
func TestAdapter_DerivedKeysEmitWhenUnclaimed(t *testing.T) {
	entry := entryWithFields()
	entry.Component = "checkout"
	entry.File = "pay.go"
	entry.Line = 42
	entry.SetError(errors.New("declined"))

	attrs := emitEntry(t, entry).attrs()
	for key, want := range map[string]string{
		attrComponent: "checkout",
		attrError:     "declined",
		attrFilePath:  "pay.go",
	} {
		if got, ok := attrs[key]; !ok || got.AsString() != want {
			t.Fatalf("%s = %v, want %q", key, attrs[key], want)
		}
	}
	if attrs[attrLineNo].AsInt64() != 42 {
		t.Fatalf("%s = %v, want 42", attrLineNo, attrs[attrLineNo])
	}
}

// An attribute with no key is not valid; the field is dropped rather than
// emitted as "".
func TestAdapter_KeylessFieldIsDropped(t *testing.T) {
	entry := entryWithFields(
		types.TypedFieldData{Val: types.StringValue("orphan")},
		types.TypedFieldData{Key: "kept", Val: types.StringValue("value")},
	)
	entry.Context = []types.TypedFieldData{{Val: types.StringValue("orphan context")}}

	pairs := attrPairs(emitEntry(t, entry))
	if n := countKey(pairs, ""); n != 0 {
		t.Fatalf("%d empty-key attributes emitted: %v", n, pairs)
	}
	if len(pairs) != 1 || pairs[0].Key != "kept" {
		t.Fatalf("keyed fields must survive the drop: %v", pairs)
	}
}

// A pre-declared key carries its name on the descriptor rather than in Key.
func TestAdapter_PreDeclaredKeyResolves(t *testing.T) {
	got := emitEntry(t, entryWithFields(
		types.TypedFieldData{KeyDesc: keyTraceFlags, Val: types.StringValue("01")},
	))
	if _, ok := got.attrs()[keyTraceFlags.Name]; !ok {
		t.Fatalf("descriptor-keyed field lost its name: %v", got.attrs())
	}
}

// A LoggerProvider may hand back a nil Logger. A logging call is the last
// place that should take the program down.
type nilLoggerProvider struct{ embedded.LoggerProvider }

func (nilLoggerProvider) Logger(string, ...otellog.LoggerOption) otellog.Logger { return nil }

func TestAdapter_NilLoggerReportsRatherThanPanics(t *testing.T) {
	a := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(nilLoggerProvider{}))

	if err := a.Health(); err == nil {
		t.Fatal("Health must report an adapter with no Logger")
	}
	if err := a.Write(entryWithFields()); err == nil {
		t.Fatal("Write must report rather than emit into a nil Logger")
	}
	if err := a.WriteZero(entryWithFields()); err == nil {
		t.Fatal("WriteZero must report rather than emit into a nil Logger")
	}
}

// Masking rewrites a field's Value; correlation must read the same resolved
// value the attributes would, not the typed original underneath it.
func TestAdapter_CorrelationReadsTheResolvedValue(t *testing.T) {
	got := emitEntry(t, entryWithFields(
		types.TypedFieldData{
			Key:   keyTraceID.Name,
			Val:   types.StringValue("00000000000000000000000000000000"),
			Value: testTraceHex,
		},
		types.TypedFieldData{Key: keySpanID.Name, Val: types.StringValue(testSpanHex)},
	))

	if !got.span.IsValid() {
		t.Fatal("correlation ignored the rewritten value and read the typed original")
	}
	if id := got.span.TraceID().String(); id != testTraceHex {
		t.Fatalf("TraceID = %s, want %s", id, testTraceHex)
	}
}

// Correlation and attributes walk one iterator, so a trace id correlates from
// whichever storage form carries it. Walking different subsets meant a
// trace_id in context storage became a plain attribute and never reached the
// record.
func TestAdapter_CorrelatesFromEveryStorageForm(t *testing.T) {
	corr := []types.TypedFieldData{
		{Key: keyTraceID.Name, Val: types.StringValue(testTraceHex)},
		{Key: keySpanID.Name, Val: types.StringValue(testSpanHex)},
	}

	cases := []struct {
		name  string
		place func(*types.LogEntry)
	}{
		{"dynamic fields", func(e *types.LogEntry) { e.Fields = corr }},
		{"context fields", func(e *types.LogEntry) { e.Context = corr }},
		{
			name: "static fields",
			place: func(e *types.LogEntry) {
				e.StaticFields = append([]types.TypedFieldData(nil), corr...)
				e.StaticFieldCount = len(corr)
			},
		},
		{
			name: "static context",
			place: func(e *types.LogEntry) {
				e.StaticContext = append([]types.TypedFieldData(nil), corr...)
				e.StaticContextCount = len(corr)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &types.LogEntry{Level: types.InfoLevel, Message: "placed"}
			tc.place(entry)

			got := emitEntry(t, entry)
			if id := got.span.TraceID().String(); id != testTraceHex {
				t.Fatalf("TraceID = %s, want %s — correlation missed this storage form", id, testTraceHex)
			}
			if n := len(attrPairs(got)); n != 0 {
				t.Fatalf("correlation fields left behind as attributes: %v", attrPairs(got))
			}
		})
	}
}

// The JSON formatter suppresses the caller for a negative line; exporting
// code.line.number: -1 would put a location on the record that no source file
// has.
func TestAdapter_NegativeLineSuppressesTheCaller(t *testing.T) {
	entry := entryWithFields()
	entry.File = "pay.go"
	entry.Line = -1

	attrs := emitEntry(t, entry).attrs()
	if _, ok := attrs[attrLineNo]; ok {
		t.Fatalf("negative line was exported: %v", attrs)
	}
	if _, ok := attrs[attrFilePath]; ok {
		t.Fatalf("file path exported without a usable line: %v", attrs)
	}
}
