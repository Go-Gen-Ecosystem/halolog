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

package core

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// adapterErrorReportInterval spaces the default reports of adapter failures.
const adapterErrorReportInterval = time.Second

// adapterErrors records the write failures of a logger's adapters; a logger
// and every child derived from it share one. Nothing here runs unless an
// adapter fails: a successful line pays one nil check of the error the
// adapter already returns.
type adapterErrors struct {
	count      atomic.Uint64
	suppressed atomic.Uint64 // failures not reported since the last report
	nextReport atomic.Int64  // UnixNano before which the default report stays quiet
	handler    func(adapter string, err error)
	out        io.Writer // where the default report goes: os.Stderr
}

// adapterFailed records that adapter a failed with err. It stays out of line
// so the logging paths do not grow for a branch they take only on failure.
//
//go:noinline
func (l *Logger) adapterFailed(a types.Adapter, err error) {
	if e := l.adapterErrs; e != nil {
		e.record(a.Name(), err)
	}
}

// record counts the failure and hands it to the handler. Without one, it
// writes it to out, at most once per adapterErrorReportInterval, with the
// number of failures not shown since the previous report.
func (e *adapterErrors) record(adapter string, err error) {
	e.count.Add(1)
	if e.handler != nil {
		e.handler(adapter, err)
		return
	}

	now := time.Now().UnixNano()
	next := e.nextReport.Load()
	if now < next || !e.nextReport.CompareAndSwap(next, now+int64(adapterErrorReportInterval)) {
		e.suppressed.Add(1)
		return
	}
	if n := e.suppressed.Swap(0); n > 0 {
		_, _ = fmt.Fprintf(e.out, "halolog: adapter %s: %v (%d earlier failures not shown)\n", adapter, err, n)
		return
	}
	_, _ = fmt.Fprintf(e.out, "halolog: adapter %s: %v\n", adapter, err)
}

// AdapterErrors returns how many adapter writes have failed for this logger
// and the loggers derived from it.
func (l *Logger) AdapterErrors() uint64 {
	if l.adapterErrs == nil {
		return 0
	}
	return l.adapterErrs.count.Load()
}
