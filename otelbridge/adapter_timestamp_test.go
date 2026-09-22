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

// Package otelbridge — record timestamp precedence.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// emitEntry drives one hand-built entry through the adapter, which is the only
// way to set the two timestamp fields independently — the logger's hot path
// always writes TimestampUnix and never the wall-clock Timestamp.
func emitEntry(t *testing.T, entry *types.LogEntry) emitted {
	t.Helper()
	rec := &recorder{}
	if err := NewAdapter("halolog/otelbridge_test", WithLoggerProvider(rec)).Write(entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return rec.only(t)
}

func TestAdapter_TimestampPrecedence(t *testing.T) {
	unix := time.Date(2026, 9, 10, 20, 18, 1, 123456789, time.UTC)
	wall := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		set  func(*types.LogEntry)
		want time.Time
	}{
		{
			name: "TimestampUnix alone",
			set:  func(e *types.LogEntry) { e.TimestampUnix = unix.UnixNano() },
			want: unix,
		},
		{
			// The precedence that matters: core writes only TimestampUnix, so
			// reading Timestamp first would date every record to the zero time.
			name: "TimestampUnix wins over Timestamp",
			set: func(e *types.LogEntry) {
				e.TimestampUnix = unix.UnixNano()
				e.Timestamp = wall
			},
			want: unix,
		},
		{
			name: "Timestamp is the fallback",
			set:  func(e *types.LogEntry) { e.Timestamp = wall },
			want: wall,
		},
		{
			name: "neither set leaves the record unstamped for the SDK",
			set:  func(*types.LogEntry) {},
			want: time.Time{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &types.LogEntry{Level: types.InfoLevel, Message: "ts"}
			tc.set(entry)

			got := emitEntry(t, entry).timestamp()
			if !got.UTC().Equal(tc.want.UTC()) {
				t.Fatalf("record timestamp = %v, want %v", got.UTC(), tc.want.UTC())
			}
		})
	}
}

func TestAdapter_TimestampThroughRealLogger(t *testing.T) {
	cases := []struct {
		name string
		emit func(*testing.T, *recorder)
	}{
		{
			name: "Typed builder",
			emit: func(t *testing.T, rec *recorder) {
				t.Helper()
				var buf bytes.Buffer
				newFanoutLogger(&buf, rec).Typed().WithInt("status", 200).Info("typed")
			},
		},
		{
			name: "Bind child",
			emit: func(t *testing.T, rec *recorder) {
				t.Helper()
				var buf bytes.Buffer
				Bind(sampledContext(), newFanoutLogger(&buf, rec)).Info("bound")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now().Add(-time.Minute)
			rec := &recorder{}
			tc.emit(t, rec)
			after := time.Now().Add(time.Minute)

			ts := rec.only(t).timestamp()
			if ts.IsZero() {
				t.Fatal("record is unstamped; the logger's TimestampUnix was not read")
			}
			if ts.Before(before) || ts.After(after) {
				t.Fatalf("record timestamp %v is not within a minute of now", ts)
			}
		})
	}
}
