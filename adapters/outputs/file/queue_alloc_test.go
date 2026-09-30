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

package file

import "testing"

// Once its buffers have grown, a Push, Take, and Done cycle allocates nothing.
func TestLineQueue_CycleAllocatesNothing(t *testing.T) {
	q := newLineQueue(1024, 4096)
	line := []byte(`{"level":"INFO","message":"steady state"}` + "\n")
	cycle := func() {
		for range 16 {
			_ = q.Push(line, 0)
		}
		chunk, lines := q.Take()
		q.Done(chunk, lines)
	}
	cycle() // grow both buffers
	cycle()
	if allocs := testing.AllocsPerRun(1000, cycle); allocs != 0 {
		t.Fatalf("a Push/Take/Done cycle allocates %.2f times, want 0", allocs)
	}
}
