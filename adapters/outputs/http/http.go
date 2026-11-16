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
// Package adapters provides output adapters
// Author: Admilson B. F. Cossa

package http

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
	"github.com/go-gen-ecosystem/halolog/utils"
)

const (
	defaultBatchSize     = 100
	defaultFlushInterval = 5 * time.Second
	defaultTimeout       = 30 * time.Second
	streamingTimeout     = 10 * time.Second

	// maxBufferedBatches bounds, in batches, the entries held while the
	// endpoint is slow or down; minBuffered keeps small batch sizes useful.
	maxBufferedBatches = 100
	minBuffered        = 1024

	retryMin = 100 * time.Millisecond // first retry delay; it doubles per failure
	retryMax = 30 * time.Second

	maxDrainBytes      = 64 << 10 // response body read so the connection is reused
	maxRetainedBytes   = 4 << 20  // larger queue buffers are dropped after a burst
	maxRetainedScratch = 64 << 10 // larger format buffers are not pooled
)

var httpStatusErrors [600]*HTTPStatusError

// HTTPAdapterOptions - Configuration for HTTP adapter. A zero or negative
// BatchSize, FlushInterval, or Timeout takes its default: 100 entries, 5 s,
// and 30 s.
//
//nolint:revive // HTTP prefix is the intended public API name; renaming would break callers across the module.
type HTTPAdapterOptions struct {
	URL           string
	Method        string
	Headers       map[string]string
	BatchSize     int
	FlushInterval time.Duration
	Timeout       time.Duration
	Formatter     types.Formatter
}

// Pre-allocated errors
var (
	ErrHTTPNilEntry  = errors.New("cannot write nil entry")
	ErrHTTPMarshal   = errors.New("failed to marshal request body")
	ErrHTTPCreateReq = errors.New("failed to create request")
	ErrHTTPSendReq   = errors.New("failed to send request")
	ErrHTTPBatch     = errors.New("failed to send batch")

	// ErrHTTPClosed is returned by Write and Flush after Close.
	ErrHTTPClosed = errors.New("http adapter is closed")
	// ErrHTTPBufferFull is returned by Write when the endpoint has fallen so
	// far behind that the buffer is full; the entry is dropped and counted.
	ErrHTTPBufferFull = errors.New("http adapter buffer is full")
	// ErrHTTPUndelivered is returned by Close when entries were never
	// delivered: dropped while running, or still unsent when Close gave up.
	ErrHTTPUndelivered = errors.New("http adapter could not deliver")
)

// HTTPStatusError represents an HTTP status error.
//
//nolint:revive // HTTP prefix is the intended public API name; renaming would break callers across the module.
type HTTPStatusError struct {
	StatusCode int
	message    string // Pre-computed
}

func (e *HTTPStatusError) Error() string {
	return e.message
}

func newHTTPStatusError(code int) error {
	if code >= 100 && code < 600 {
		return httpStatusErrors[code]
	}
	return &HTTPStatusError{
		StatusCode: code,
		message:    buildErrorMessage(code),
	}
}

// HTTPAdapter sends log entries to an HTTP endpoint in JSON batches.
//
// Write formats the entry into a bounded queue and returns; it never keeps
// the entry. One delivery goroutine sends a batch when BatchSize entries
// wait, every FlushInterval, and on Flush. A batch that fails is retried, in
// order, with exponential backoff, and nothing behind it is lost; a batch the
// endpoint rejects with a 4xx other than 408 or 429 is dropped and counted.
// With BatchSize 1 (streaming), Write delivers before it returns.
//
//nolint:revive // HTTP prefix is the intended public API name; renaming would break callers across the module.
type HTTPAdapter struct {
	url           string
	method        string
	header        http.Header
	batchSize     int
	flushInterval time.Duration
	timeout       time.Duration
	client        *http.Client
	adapterType   string // Track adapter type for Name() method

	// Formatter, swappable at runtime. Behind an atomic pointer so a
	// concurrent SetFormatter never tears the interface value a Write reads.
	formatter atomic.Pointer[types.Formatter]
	scratch   sync.Pool // *[]byte format buffers

	queue    *recordQueue
	flushReq chan chan error
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	closeOnce sync.Once
	closeErr  error // set by the delivery goroutine before it exits

	unsent  atomic.Int64          // entries accepted and not yet delivered or dropped
	dropped atomic.Int64          // entries lost: buffer full or rejected by the endpoint
	health  atomic.Pointer[error] // the latest failure; nil after a delivery

	// Owned by the delivery goroutine.
	pending pendingChunk
	body    bytes.Buffer
	backoff time.Duration
	retryAt time.Time
}

func init() {
	for i := 100; i < 600; i++ {
		httpStatusErrors[i] = &HTTPStatusError{
			StatusCode: i,
			message:    buildErrorMessage(i),
		}
	}
}

// NewHTTPAdapterWithOptions creates an HTTP adapter and starts its delivery
// goroutine; Close stops it.
func NewHTTPAdapterWithOptions(options *HTTPAdapterOptions) *HTTPAdapter {
	if options == nil {
		options = &HTTPAdapterOptions{}
	}
	ctx, cancel := context.WithCancel(context.Background())

	method := options.Method
	if method == "" {
		method = http.MethodPost
	}
	batchSize := orDefault(options.BatchSize, defaultBatchSize)
	timeout := orDefault(options.Timeout, defaultTimeout)
	headers := options.Headers
	if headers == nil {
		headers = map[string]string{"Content-Type": "application/json"}
	}
	header := make(http.Header, len(headers))
	for k, v := range headers {
		header.Set(k, v)
	}

	// Streaming delivers from Write itself, so it never wakes the goroutine.
	threshold := batchSize
	if batchSize == 1 {
		threshold = math.MaxInt
	}

	adapter := &HTTPAdapter{
		url:           options.URL,
		method:        method,
		header:        header,
		batchSize:     batchSize,
		flushInterval: orDefault(options.FlushInterval, defaultFlushInterval),
		timeout:       timeout,
		adapterType:   "HTTPAdapter",
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				MaxConnsPerHost:     100,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
		},
		queue:    newRecordQueue(max(maxBufferedBatches*batchSize, minBuffered), threshold),
		flushReq: make(chan chan error),
		done:     make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
	adapter.scratch.New = func() any {
		b := make([]byte, 0, 512)
		return &b
	}

	if options.Formatter != nil {
		adapter.SetFormatter(options.Formatter)
	} else {
		adapter.SetFormatter(NewZeroJSONFormatter())
	}

	adapter.wg.Add(1)
	go adapter.StartHttpFlushTimer()
	return adapter
}

// Write formats entry into the queue; the caller may reuse entry as soon as
// it returns. It returns ErrHTTPBufferFull when the endpoint has fallen too
// far behind and ErrHTTPClosed after Close. With BatchSize 1 it returns the
// delivery error.
func (a *HTTPAdapter) Write(entry *types.LogEntry) error {
	if entry == nil {
		return ErrHTTPNilEntry
	}

	p := a.scratch.Get().(*[]byte)
	record := bytes.TrimSuffix((*a.formatter.Load()).Format(entry, (*p)[:0]), newline)
	var err error
	if len(record) > 0 {
		a.unsent.Add(1)
		if err = a.queue.Push(record); err != nil {
			a.unsent.Add(-1)
		}
	}
	if cap(record) <= maxRetainedScratch {
		*p = record[:0]
		a.scratch.Put(p)
	}

	switch {
	case errors.Is(err, ErrHTTPBufferFull):
		a.dropped.Add(1)
		a.setHealth(err)
		return err
	case err != nil:
		return err
	case a.batchSize == 1:
		return a.Flush()
	}
	return nil
}

var newline = []byte{'\n'}

// Flush tries once to deliver every entry written before it and returns the
// first failure. Entries that failed to send stay queued for a retry, unless
// the endpoint rejected them for good.
func (a *HTTPAdapter) Flush() error {
	reply := make(chan error, 1)
	select {
	case a.flushReq <- reply:
		return <-reply
	case <-a.done:
		return ErrHTTPClosed
	}
}

// Close refuses further entries, makes a last attempt to deliver what is
// queued, bounded by the request timeout, and releases its connections. It
// returns ErrHTTPUndelivered, with the count, if any entry was never
// delivered. Calling it again returns the same result.
func (a *HTTPAdapter) Close() error {
	a.closeOnce.Do(func() {
		a.queue.Close()
		close(a.done)
		a.wg.Wait()
		a.cancel()
		a.client.CloseIdleConnections()
	})
	return a.closeErr
}

// WriteZero implements types.Adapter; it is Write.
func (a *HTTPAdapter) WriteZero(entry *types.LogEntry) error {
	return a.Write(entry)
}

// Health returns nil while deliveries succeed, the latest failure while they
// do not, and context.Canceled after Close.
func (a *HTTPAdapter) Health() error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if err := a.health.Load(); err != nil {
		return *err
	}
	return nil
}

func (a *HTTPAdapter) setHealth(err error) { a.health.Store(&err) }

// Name - Returns the adapter name
func (a *HTTPAdapter) Name() string {
	return a.adapterType
}

// SetFormatter sets the formatter for the adapter (nil is ignored). Safe to
// call concurrently with Write.
func (a *HTTPAdapter) SetFormatter(formatter types.Formatter) {
	if formatter == nil {
		return
	}
	a.formatter.Store(&formatter)
}

// NewHTTPAdapter - Creates a new HTTP adapter with default options
func NewHTTPAdapter(url string) *HTTPAdapter {
	return NewHTTPAdapterWithOptions(&HTTPAdapterOptions{URL: url})
}

// NewStreamingHTTPAdapter creates an adapter that delivers each entry before
// Write returns. A zero timeout means 10 s.
func NewStreamingHTTPAdapter(url string, timeout time.Duration) *HTTPAdapter {
	adapter := NewHTTPAdapterWithOptions(&HTTPAdapterOptions{
		URL:       url,
		Timeout:   orDefault(timeout, streamingTimeout),
		BatchSize: 1,
	})
	adapter.adapterType = "StreamingHTTPAdapter"
	return adapter
}

// orDefault returns v, or def when v is zero or negative.
func orDefault[T ~int | ~int64](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

func buildErrorMessage(code int) string {
	return utils.FormatIntWithPrefix("HTTP error: ", code)
}
