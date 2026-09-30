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
	"errors"
	"testing"
	"time"
)

func mustPush(t *testing.T, q *lineQueue, line string) {
	t.Helper()
	if err := q.Push([]byte(line), 0); err != nil {
		t.Fatalf("Push(%q): %v", line, err)
	}
}

// Take returns every pushed line, in push order, and signals Ready first.
func TestLineQueue_TakeReturnsLinesInPushOrder(t *testing.T) {
	q := newLineQueue(8, 0)
	mustPush(t, q, "a\n")
	mustPush(t, q, "b\n")
	select {
	case <-q.Ready():
	default:
		t.Fatal("Ready had no token after Push")
	}
	chunk, lines := q.Take()
	if string(chunk) != "a\nb\n" || lines != 2 {
		t.Fatalf("Take = %q, %d lines; want \"a\\nb\\n\", 2", chunk, lines)
	}
	q.Done(chunk, lines)
	if chunk, lines := q.Take(); chunk != nil || lines != 0 {
		t.Fatalf("Take on an empty queue = %q, %d", chunk, lines)
	}
}

// A full queue makes Push wait for room, then give up after the timeout.
// Lines taken but not yet returned by Done still count against the capacity.
func TestLineQueue_PushTimesOutWhileFull(t *testing.T) {
	q := newLineQueue(2, 0)
	mustPush(t, q, "a\n")
	chunk, lines := q.Take() // one line in flight
	mustPush(t, q, "b\n")

	start := time.Now()
	if err := q.Push([]byte("c\n"), 20*time.Millisecond); !errors.Is(err, ErrFileQueueFull) {
		t.Fatalf("Push into a full queue = %v, want ErrFileQueueFull", err)
	}
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("Push gave up after %v, before its 20ms timeout", waited)
	}
	q.Done(chunk, lines)
	if err := q.Push([]byte("c\n"), 0); err != nil {
		t.Fatalf("Push after Done freed room: %v", err)
	}
}

// Done wakes a producer waiting for room.
func TestLineQueue_DoneWakesAWaitingPush(t *testing.T) {
	q := newLineQueue(1, 0)
	mustPush(t, q, "a\n")
	chunk, lines := q.Take()

	result := make(chan error, 1)
	go func() { result <- q.Push([]byte("b\n"), 5*time.Second) }()
	waitForWaiters(t, q)
	q.Done(chunk, lines)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the waiting Push failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Done did not wake the waiting Push")
	}
}

// Close wakes a waiting producer with ErrFileClosed, refuses later lines, and
// leaves queued lines to be taken.
func TestLineQueue_CloseRefusesAndWakes(t *testing.T) {
	q := newLineQueue(1, 0)
	mustPush(t, q, "a\n")

	result := make(chan error, 1)
	go func() { result <- q.Push([]byte("b\n"), 5*time.Second) }()
	waitForWaiters(t, q)
	q.Close()

	select {
	case err := <-result:
		if !errors.Is(err, ErrFileClosed) {
			t.Fatalf("the waiting Push = %v, want ErrFileClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake the waiting Push")
	}
	if err := q.Push([]byte("c\n"), 0); !errors.Is(err, ErrFileClosed) {
		t.Fatalf("Push after Close = %v, want ErrFileClosed", err)
	}
	if chunk, lines := q.Take(); string(chunk) != "a\n" || lines != 1 {
		t.Fatalf("Take after Close = %q, %d; want the queued line", chunk, lines)
	}
}

// waitForWaiters blocks until a producer is waiting for room.
func waitForWaiters(t *testing.T, q *lineQueue) {
	t.Helper()
	if !waitFor(time.Second, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.waiters > 0
	}) {
		t.Fatal("no producer started waiting for room")
	}
}
