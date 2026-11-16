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

// Allocation contracts run without the race detector, like the others in
// this module: its instrumentation changes allocation counts.
//go:build !race

package http

import (
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// Once warm, Write formats and queues an entry without allocating, and so
// does the queue's Take and Release cycle.
func TestHTTPAdapter_WriteAllocatesNothing(t *testing.T) {
	a := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:           "http://127.0.0.1:1",
		BatchSize:     1 << 16, // never woken by a full batch
		FlushInterval: time.Hour,
	})
	defer func() { _ = a.Close() }()

	entry := &types.LogEntry{
		Level:         types.InfoLevel,
		Message:       `steady "state"`,
		TimestampUnix: 1_700_000_000_000_000_000,
		Fields: []types.TypedFieldData{
			{Key: "user", Val: types.StringValue("alice")},
			{Key: "status", Val: types.Int64Value(200)},
			{Key: "legacy", Value: "interface value"},
		},
	}
	cycle := func() {
		for range 16 {
			_ = a.Write(entry)
		}
		buf, ends := a.queue.Take(0)
		a.queue.Release(buf, ends)
	}
	cycle() // grow the buffers
	cycle()
	if allocs := testing.AllocsPerRun(1000, cycle); allocs != 0 {
		t.Fatalf("a Write and queue cycle allocates %.2f times, want 0", allocs)
	}
}
