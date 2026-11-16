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
	"errors"
	"testing"
)

func mustPush(t *testing.T, q *recordQueue, records ...string) {
	t.Helper()
	for _, r := range records {
		if err := q.Push([]byte(r)); err != nil {
			t.Fatalf("Push(%q): %v", r, err)
		}
	}
}

// Take with a batch size takes whole batches only and keeps the partial
// batch queued, in order, for the next Take.
func TestRecordQueue_TakeLeavesThePartialBatchQueued(t *testing.T) {
	q := newRecordQueue(16, 2)
	mustPush(t, q, `{"a":1}`, `{"b":2}`, `{"c":3}`)

	buf, ends := q.Take(2)
	if string(buf) != `{"a":1},{"b":2},` || len(ends) != 2 || ends[1] != len(buf) {
		t.Fatalf("Take(2) = %q, ends %v", buf, ends)
	}
	q.Release(buf, ends)

	if buf, ends := q.Take(2); buf != nil {
		t.Fatalf("Take(2) with one record queued = %q, %v", buf, ends)
	}
	buf, ends = q.Take(0)
	if string(buf) != `{"c":3},` || len(ends) != 1 || ends[0] != len(buf) {
		t.Fatalf("Take(0) = %q, ends %v", buf, ends)
	}
}

// Records taken and not yet released still count against the capacity.
func TestRecordQueue_CapacityCountsRecordsInFlight(t *testing.T) {
	q := newRecordQueue(2, 2)
	mustPush(t, q, "1", "2")
	buf, ends := q.Take(0)
	if err := q.Push([]byte("3")); !errors.Is(err, ErrHTTPBufferFull) {
		t.Fatalf("Push with two records in flight = %v, want ErrHTTPBufferFull", err)
	}
	q.Release(buf, ends)
	mustPush(t, q, "3")
}

// A full batch leaves a wake-up token; a partial one does not.
func TestRecordQueue_ReadyOnceABatchIsFull(t *testing.T) {
	q := newRecordQueue(16, 2)
	mustPush(t, q, "1")
	select {
	case <-q.ready:
		t.Fatal("ready after one record of a two-record batch")
	default:
	}
	mustPush(t, q, "2")
	select {
	case <-q.ready:
	default:
		t.Fatal("no ready token after a full batch")
	}
}

// A closed queue refuses records and still hands out the queued ones.
func TestRecordQueue_CloseRefusesNewRecords(t *testing.T) {
	q := newRecordQueue(16, 2)
	mustPush(t, q, "1")
	q.Close()
	if err := q.Push([]byte("2")); !errors.Is(err, ErrHTTPClosed) {
		t.Fatalf("Push after Close = %v, want ErrHTTPClosed", err)
	}
	if buf, _ := q.Take(0); string(buf) != "1," {
		t.Fatalf("Take after Close = %q", buf)
	}
}
