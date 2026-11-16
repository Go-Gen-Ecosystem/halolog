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

import "sync"

// recordQueue holds formatted records until the delivery goroutine takes
// them. Each record is stored followed by a comma, so a run of records is
// already the body of a JSON array once its last comma is dropped. Producers
// append under a mutex; the consumer takes the whole buffer at once and hands
// it back when it is delivered, so two buffers alternate. Push, Take, and
// Release are O(1) apart from copying the record, and allocate nothing once
// the buffers have grown.
type recordQueue struct {
	mu        sync.Mutex
	buf       []byte // records pushed since the last Take, each followed by ','
	ends      []int  // end offset in buf of each record, after its comma
	spareBuf  []byte // buffers returned by Release, reused by the next Take
	spareEnds []int
	inflight  int // records taken and not yet released
	capacity  int // queued + inflight records never exceed it
	threshold int // a Push that leaves this many records queued signals ready
	closed    bool
	ready     chan struct{} // holds a token while a full batch waits
}

func newRecordQueue(capacity, threshold int) *recordQueue {
	return &recordQueue{
		capacity:  capacity,
		threshold: threshold,
		ready:     make(chan struct{}, 1),
	}
}

// Push appends one record. It returns ErrHTTPBufferFull when the queue holds
// its capacity and ErrHTTPClosed once the queue is closed; it never blocks.
func (q *recordQueue) Push(record []byte) error {
	q.mu.Lock()
	switch {
	case q.closed:
		q.mu.Unlock()
		return ErrHTTPClosed
	case len(q.ends)+q.inflight >= q.capacity:
		q.mu.Unlock()
		return ErrHTTPBufferFull
	}
	q.buf = append(append(q.buf, record...), ',')
	q.ends = append(q.ends, len(q.buf))
	full := len(q.ends) >= q.threshold
	q.mu.Unlock()

	if full {
		select {
		case q.ready <- struct{}{}:
		default: // a wake-up is already pending
		}
	}
	return nil
}

// Take hands the consumer the queued records, with ends[i] the offset just
// past record i's comma. A positive batch takes only whole batches and leaves
// the partial one queued; moving it costs less than one batch. The consumer
// gives both slices back with Release.
func (q *recordQueue) Take(batch int) (buf []byte, ends []int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	n := len(q.ends)
	if batch > 0 {
		n -= n % batch
	}
	if n == 0 {
		return nil, nil
	}
	buf, ends = q.buf, q.ends
	rest, restEnds := q.spareBuf[:0], q.spareEnds[:0]
	if n < len(ends) {
		cut := ends[n-1]
		rest = append(rest, buf[cut:]...)
		for _, end := range ends[n:] {
			restEnds = append(restEnds, end-cut)
		}
		buf, ends = buf[:cut], ends[:n]
	}
	q.buf, q.ends = rest, restEnds
	q.spareBuf, q.spareEnds = nil, nil
	q.inflight += n
	return buf, ends
}

// Release returns the slices from Take once their records are delivered or
// dropped, freeing their room. Buffers grown past maxRetainedBytes by a burst
// are left to the garbage collector.
func (q *recordQueue) Release(buf []byte, ends []int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.inflight -= len(ends)
	if cap(buf) <= maxRetainedBytes {
		q.spareBuf, q.spareEnds = buf[:0], ends[:0]
	}
}

// Close refuses further records; queued records can still be taken.
func (q *recordQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}
