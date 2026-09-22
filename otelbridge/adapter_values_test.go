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

// Package otelbridge — scalar conversion and payload ownership.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
)

func TestAnyValue_ScalarWidths(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want otellog.Value
	}{
		{"int", int(-7), otellog.Int64Value(-7)},
		{"int8", int8(math.MinInt8), otellog.Int64Value(math.MinInt8)},
		{"int16", int16(math.MinInt16), otellog.Int64Value(math.MinInt16)},
		{"int32", int32(math.MinInt32), otellog.Int64Value(math.MinInt32)},
		{"int64", int64(math.MinInt64), otellog.Int64Value(math.MinInt64)},
		{"uint", uint(7), otellog.Int64Value(7)},
		{"uint8", uint8(math.MaxUint8), otellog.Int64Value(math.MaxUint8)},
		{"uint16", uint16(math.MaxUint16), otellog.Int64Value(math.MaxUint16)},
		{"uint32", uint32(math.MaxUint32), otellog.Int64Value(math.MaxUint32)},
		{"uint64 within int64", uint64(math.MaxInt64), otellog.Int64Value(math.MaxInt64)},
		{"float32", float32(0.5), otellog.Float64Value(0.5)},
		{"float64", float64(0.25), otellog.Float64Value(0.25)},
		{"bool", true, otellog.BoolValue(true)},
		{"string", "s", otellog.StringValue("s")},
		{"error", errors.New("boom"), otellog.StringValue("boom")},
		{"nil stays empty", nil, otellog.Value{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyValue(tc.in); !got.Equal(tc.want) {
				t.Fatalf("anyValue(%#v) = %v (%s), want %v (%s)",
					tc.in, got, got.Kind(), tc.want, tc.want.Kind())
			}
		})
	}
}

// float32 must widen through its own value, not through a decimal detour:
// float64(float32(0.1)) is 0.10000000149011612 and that is the exact number
// the record should carry.
func TestAnyValue_Float32WidensExactly(t *testing.T) {
	got := anyValue(float32(0.1))
	if want := float64(float32(0.1)); got.AsFloat64() != want {
		t.Fatalf("anyValue(float32(0.1)) = %v, want %v", got.AsFloat64(), want)
	}
}

// The log API has no unsigned kind. Values above MaxInt64 would wrap negative
// if cast, so they become exact decimal strings instead.
func TestAnyValue_LargeUnsignedStaysExact(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"uint64 max", uint64(math.MaxUint64), "18446744073709551615"},
		{"one past int64 max", uint64(math.MaxInt64) + 1, "9223372036854775808"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := anyValue(tc.in)
			if got.Kind() != otellog.KindString {
				t.Fatalf("kind = %s, want String so the value cannot wrap", got.Kind())
			}
			if got.AsString() != tc.want {
				t.Fatalf("anyValue(%v) = %q, want %q", tc.in, got.AsString(), tc.want)
			}
		})
	}
}

func TestAdapter_LargeUnsignedThroughTheLogger(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	newFanoutLogger(&buf, rec).WithField("bytes_seen", uint64(math.MaxUint64)).Info("counted")

	got := rec.only(t).attrs()["bytes_seen"]
	if got.AsString() != "18446744073709551615" {
		t.Fatalf("bytes_seen = %v (%s), want the exact decimal string", got, got.Kind())
	}
}

// log.BytesValue keeps a pointer to the caller's array instead of copying it.
// Records outlive the logging call — the SDK holds them in its batch queue —
// so the adapter copies, and a caller reusing its buffer must not be able to
// rewrite a record already emitted.
func TestAdapter_RetainedRecordOwnsItsBytes(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	payload := []byte("original")

	newFanoutLogger(&buf, rec).WithField("payload", payload).Info("sent")

	copy(payload, "MUTATED!")

	got := rec.only(t).attrs()["payload"]
	if string(got.AsBytes()) != "original" {
		t.Fatalf("mutating the caller's slice rewrote the retained record: %q", got.AsBytes())
	}
	if string(payload) != "MUTATED!" {
		t.Fatalf("test did not actually mutate the source slice: %q", payload)
	}
}

func TestAdapter_NilByteSliceIsEmpty(t *testing.T) {
	if got := anyValue([]byte(nil)); !got.Empty() {
		t.Fatalf("anyValue([]byte(nil)) = %v, want the empty value", got)
	}
}

// logValue's own precedence rule, tested directly on fields this test builds.
// It asserts the adapter tolerates a value rewritten after the builder ran —
// not that core stores it in any particular slot, so a repair that makes
// either slot canonical leaves this passing.
func TestLogValue_MaskedValueWinsOverTypedOriginal(t *testing.T) {
	masked := types.TypedFieldData{
		Key:   "password",
		Val:   types.StringValue("hunter2"),
		Value: "***PASSWORD***",
	}
	if got := logValue(masked).AsString(); got != "***PASSWORD***" {
		t.Fatalf("logValue = %q, want the masked replacement", got)
	}

	untouched := types.TypedFieldData{Key: "user", Val: types.StringValue("alice")}
	if got := logValue(untouched).AsString(); got != "alice" {
		t.Fatalf("logValue = %q, want the typed value", got)
	}
}
