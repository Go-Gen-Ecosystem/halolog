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

package file

import (
	"sync"
	"time"
)

// lineQueue is a bounded queue of formatted lines with many producers and one
// consumer. Producers append to a single buffer under a mutex; the consumer
// takes the whole buffer at once and hands it back when the lines are
// written, so two buffers alternate. Push, Take, and Done are O(1) apart from
// copying the line, and allocate nothing once the buffers have grown.
type lineQueue struct {
	mu       sync.Mutex
	buf      []byte // lines pushed since the last Take
	spare    []byte // the buffer returned by the last Done, reused by the next Take
	queued   int    // lines in buf
	inflight int    // lines taken and not yet returned by Done
	capacity int    // queued + inflight never exceeds it
	closed   bool
	waiters  int           // producers waiting for room
	room     chan struct{} // closed, then replaced, when room frees up for waiters
	ready    chan struct{} // holds a token while lines wait for the consumer
}

func newLineQueue(capacity, initialBytes int) *lineQueue {
	return &lineQueue{
		buf:      make([]byte, 0, initialBytes),
		capacity: capacity,
		room:     make(chan struct{}),
		ready:    make(chan struct{}, 1),
	}
}

// Push appends line, waiting up to timeout while the queue is full. It returns
// ErrFileQueueFull when the wait times out and ErrFileClosed once the queue is
// closed.
func (q *lineQueue) Push(line []byte, timeout time.Duration) error {
	q.mu.Lock()
	if q.closed || q.queued+q.inflight >= q.capacity {
		if err := q.waitForRoom(timeout); err != nil {
			q.mu.Unlock()
			return err
		}
	}
	q.buf = append(q.buf, line...)
	q.queued++
	q.mu.Unlock()

	select {
	case q.ready <- struct{}{}:
	default: // the consumer already has a wake-up pending
	}
	return nil
}

// waitForRoom waits, releasing mu meanwhile, until the queue has room, is
// closed, or timeout passes. It is called and returns with mu held.
func (q *lineQueue) waitForRoom(timeout time.Duration) error {
	if q.closed {
		return ErrFileClosed
	}
	if timeout <= 0 {
		return ErrFileQueueFull
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		room := q.room
		q.waiters++
		q.mu.Unlock()
		var expired bool
		select {
		case <-room:
		case <-timer.C:
			expired = true
		}
		q.mu.Lock()
		q.waiters--

		switch {
		case q.closed:
			return ErrFileClosed
		case q.queued+q.inflight < q.capacity:
			return nil
		case expired:
			return ErrFileQueueFull
		}
	}
}

// Take hands the consumer every queued line in one buffer, with the number of
// lines in it. The consumer gives the buffer back with Done once it is written.
func (q *lineQueue) Take() ([]byte, int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.queued == 0 {
		return nil, 0
	}
	chunk, lines := q.buf, q.queued
	q.buf, q.spare, q.queued = q.spare[:0], nil, 0
	q.inflight += lines
	return chunk, lines
}

// Done returns a buffer from Take once its lines are written. Their room is
// freed, and the buffer is kept for reuse unless a burst grew it past
// maxRetainedQueueBytes.
func (q *lineQueue) Done(chunk []byte, lines int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.inflight -= lines
	if cap(chunk) <= maxRetainedQueueBytes {
		q.spare = chunk[:0]
	}
	q.wakeWaiters()
}

// Close refuses further lines and wakes the producers waiting for room. Lines
// already queued can still be taken.
func (q *lineQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.closed = true
	q.wakeWaiters()
}

// Ready delivers a token when lines wait for the consumer.
func (q *lineQueue) Ready() <-chan struct{} {
	return q.ready
}

// wakeWaiters releases every producer waiting for room. Called with mu held;
// it allocates only when there is someone to wake.
func (q *lineQueue) wakeWaiters() {
	if q.waiters > 0 {
		close(q.room)
		q.room = make(chan struct{})
	}
}
