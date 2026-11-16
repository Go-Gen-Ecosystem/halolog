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
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// pendingChunk is the run of records the delivery goroutine has taken from the
// queue and not yet finished: records before next are delivered or dropped.
type pendingChunk struct {
	buf  []byte
	ends []int
	next int
}

func (p *pendingChunk) remaining() int { return len(p.ends) - p.next }

// deliveryMode says what an attempt may send and whether it waits out the
// backoff.
type deliveryMode uint8

const (
	wholeBatches deliveryMode = iota // a batch filled up: send whole batches only
	everything                       // timer or retry: send all, after the backoff
	immediately                      // Flush or Close: send all now
)

// StartHttpFlushTimer runs the delivery loop: the only goroutine that sends
// requests. The constructor starts it; it returns when the adapter closes.
func (a *HTTPAdapter) StartHttpFlushTimer() {
	defer a.wg.Done()

	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()
	retry := time.NewTimer(retryMax)
	retry.Stop()
	defer retry.Stop()

	for {
		select {
		case <-a.queue.ready:
			_ = a.deliver(a.ctx, wholeBatches) // failures are recorded for Health
		case <-ticker.C:
			_ = a.deliver(a.ctx, everything)
		case <-retry.C:
			_ = a.deliver(a.ctx, everything)
		case reply := <-a.flushReq:
			reply <- a.deliver(a.ctx, immediately)
		case <-a.done:
			a.closeErr = a.drain()
			return
		}
		if a.backoff > 0 {
			retry.Reset(time.Until(a.retryAt))
		}
	}
}

// deliver sends the pending chunk and then the records queued now, in
// requests of at most batchSize records. A retryable failure stops it and
// keeps the unsent records, in order, for the next attempt; records the
// endpoint rejects for good are dropped and counted, and delivery goes on.
// It returns the first failure of this attempt.
func (a *HTTPAdapter) deliver(ctx context.Context, mode deliveryMode) error {
	if mode != immediately && a.backoff > 0 && time.Now().Before(a.retryAt) {
		return nil
	}
	batch := 0
	if mode == wholeBatches {
		batch = a.batchSize
	}

	var firstErr error
	p := &a.pending
	for took := false; ; {
		if p.remaining() == 0 {
			if p.ends != nil {
				a.queue.Release(p.buf, p.ends)
				*p = pendingChunk{}
			}
			if took {
				return firstErr
			}
			p.buf, p.ends = a.queue.Take(batch)
			took = true
			if p.ends == nil {
				return firstErr
			}
		}

		end := min(p.next+a.batchSize, len(p.ends))
		err := a.send(ctx, p, end)
		switch {
		case err == nil:
			a.unsent.Add(int64(p.next - end))
			a.backoff = 0
			a.health.Store(nil)
		case isPermanent(err):
			a.unsent.Add(int64(p.next - end))
			a.dropped.Add(int64(end - p.next))
			a.setHealth(err)
		default:
			a.backoff = min(max(2*a.backoff, retryMin), retryMax)
			a.retryAt = time.Now().Add(a.backoff)
			a.setHealth(err)
			return cmp.Or(firstErr, err)
		}
		firstErr = cmp.Or(firstErr, err)
		p.next = end
	}
}

// drain makes the final delivery attempt for everything queued, bounded by
// the client timeout, and reports every line that was never delivered.
func (a *HTTPAdapter) drain() error {
	ctx, cancel := context.WithTimeout(a.ctx, a.timeout)
	defer cancel()

	err := a.deliver(ctx, immediately)
	lost := a.dropped.Load() + a.unsent.Load()
	if lost == 0 {
		return nil
	}
	if err == nil {
		return fmt.Errorf("%w: %d lines", ErrHTTPUndelivered, lost)
	}
	return fmt.Errorf("%w: %d lines: %w", ErrHTTPUndelivered, lost, err)
}

// send posts records [p.next, end) of the pending chunk as one batch.
func (a *HTTPAdapter) send(ctx context.Context, p *pendingChunk, end int) error {
	start := 0
	if p.next > 0 {
		start = p.ends[p.next-1]
	}
	records := p.buf[start : p.ends[end-1]-1] // without the last comma

	var num [20]byte
	a.body.Reset()
	a.body.WriteString(`{"timestamp":`)
	a.body.Write(strconv.AppendInt(num[:0], time.Now().Unix(), 10))
	a.body.WriteString(`,"count":`)
	a.body.Write(strconv.AppendInt(num[:0], int64(end-p.next), 10))
	a.body.WriteString(`,"entries":[`)
	a.body.Write(records)
	a.body.WriteString("]}")

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, a.method, a.url, bytes.NewReader(a.body.Bytes()))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHTTPCreateReq, err)
	}
	req.Header = a.header.Clone()

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHTTPSendReq, err)
	}
	// Drain a bounded amount so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	_ = resp.Body.Close()

	if resp.StatusCode/100 == 2 {
		return nil
	}
	return newHTTPStatusError(resp.StatusCode)
}

// isPermanent reports whether retrying err cannot succeed: the request could
// not be built, or the endpoint rejected it with a status other than 408, 429,
// or 5xx.
func isPermanent(err error) bool {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		code := status.StatusCode
		return code < http.StatusInternalServerError &&
			code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
	}
	return errors.Is(err, ErrHTTPCreateReq)
}
