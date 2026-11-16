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
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/types"
)

var errBoom = errors.New("boom")

// failingAdapter fails every write.
type failingAdapter struct{ name string }

func (f *failingAdapter) Name() string                    { return f.name }
func (f *failingAdapter) Write(*types.LogEntry) error     { return errBoom }
func (f *failingAdapter) WriteZero(*types.LogEntry) error { return errBoom }
func (f *failingAdapter) Flush() error                    { return nil }
func (f *failingAdapter) Close() error                    { return nil }
func (f *failingAdapter) SetFormatter(types.Formatter)    {}
func (f *failingAdapter) Health() error                   { return errBoom }

// failingWriter fails every write, so a console adapter on it fails on the
// direct path.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errBoom }

// passMasker masks nothing; it only puts the logger on its masked paths.
type passMasker struct{ types.PIIMasker }

func (passMasker) Apply(*types.LogEntry) {}

// recorder collects the failures a logger reports.
type recorder struct {
	mu    sync.Mutex
	names []string
}

func (r *recorder) record(adapter string, err error) {
	if !errors.Is(err, errBoom) {
		panic("unexpected error: " + err.Error())
	}
	r.mu.Lock()
	r.names = append(r.names, adapter)
	r.mu.Unlock()
}

// Every path that hands a line to an adapter reports the adapter's failure,
// once per adapter and line, and counts it.
func TestLogger_ReportsAdapterFailures(t *testing.T) {
	setups := map[string]func(onError func(string, error)) (*Logger, int){
		"direct path": func(onError func(string, error)) (*Logger, int) {
			a := console.NewWithWriter(failingWriter{}, jsonfmt.NewJsonFormatter())
			return NewLogger(Config{Level: types.TraceLevel, Adapters: []types.Adapter{a}, OnAdapterError: onError}), 1
		},
		"one adapter": func(onError func(string, error)) (*Logger, int) {
			return NewLogger(Config{Level: types.TraceLevel, Adapters: []types.Adapter{&failingAdapter{name: "a"}}, OnAdapterError: onError}), 1
		},
		"two adapters": func(onError func(string, error)) (*Logger, int) {
			adapters := []types.Adapter{&failingAdapter{name: "a"}, &failingAdapter{name: "b"}}
			return NewLogger(Config{Level: types.TraceLevel, Adapters: adapters, OnAdapterError: onError}), 2
		},
		"masked, one adapter": func(onError func(string, error)) (*Logger, int) {
			return NewLogger(Config{Level: types.TraceLevel, Adapters: []types.Adapter{&failingAdapter{name: "a"}},
				EnableMasking: true, Masker: passMasker{}, OnAdapterError: onError}), 1
		},
		"masked, two adapters": func(onError func(string, error)) (*Logger, int) {
			adapters := []types.Adapter{&failingAdapter{name: "a"}, &failingAdapter{name: "b"}}
			return NewLogger(Config{Level: types.TraceLevel, Adapters: adapters,
				EnableMasking: true, Masker: passMasker{}, OnAdapterError: onError}), 2
		},
	}
	lines := map[string]func(l *Logger){
		"Trace":     func(l *Logger) { l.Trace("m") },
		"Debug":     func(l *Logger) { l.Debug("m") },
		"Info":      func(l *Logger) { l.Info("m") },
		"Warn":      func(l *Logger) { l.Warn("m") },
		"Error":     func(l *Logger) { l.Error("m") },
		"WithField": func(l *Logger) { l.WithField("k", 1).Info("m") },
		"Typed":     func(l *Logger) { l.Typed().WithInt("k", 1).Info("m") },
		"InfoLine":  func(l *Logger) { l.InfoLine().WithInt("k", 1).Msg("m") },
		"child":     func(l *Logger) { l.With().WithString("svc", "x").Logger().Info("m") },
		"child line": func(l *Logger) {
			l.With().WithString("svc", "x").Logger().WithField("k", 1).Info("m")
		},
	}
	for setupName, setup := range setups {
		for lineName, log := range lines {
			t.Run(setupName+"/"+lineName, func(t *testing.T) {
				var rec recorder
				l, adapters := setup(rec.record)
				log(l)
				if len(rec.names) != adapters {
					t.Fatalf("reported %d failures %v, want one per adapter (%d)", len(rec.names), rec.names, adapters)
				}
				if got := l.AdapterErrors(); got != uint64(adapters) {
					t.Fatalf("AdapterErrors = %d, want %d", got, adapters)
				}
			})
		}
	}
}

// A child logger shares its parent's count.
func TestLogger_ChildSharesAdapterErrorCount(t *testing.T) {
	l := NewLogger(Config{Level: types.InfoLevel, Adapters: []types.Adapter{&failingAdapter{name: "a"}}, OnAdapterError: func(string, error) {}})
	child := l.With().WithString("svc", "x").Logger()
	l.Info("parent")
	child.Info("child")
	if l.AdapterErrors() != 2 || child.AdapterErrors() != 2 {
		t.Fatalf("parent counts %d, child %d; want 2 for both", l.AdapterErrors(), child.AdapterErrors())
	}
}

// Without a handler, failures go to stderr at most once per interval, with
// the number of failures not shown since the last report.
func TestLogger_DefaultReportIsRateLimited(t *testing.T) {
	l := NewLogger(Config{Level: types.InfoLevel, Adapters: []types.Adapter{&failingAdapter{name: "a"}}})
	var out bytes.Buffer
	l.adapterErrs.out = &out

	for range 100 {
		l.Info("m")
	}
	if got := out.String(); got != "halolog: adapter a: boom\n" {
		t.Fatalf("100 failures reported as %q, want one line", got)
	}

	l.adapterErrs.nextReport.Store(0) // the interval has passed
	l.Info("m")
	if got := strings.Count(out.String(), "\n"); got != 2 || !strings.Contains(out.String(), "(99 earlier failures not shown)") {
		t.Fatalf("report after the interval: %q", out.String())
	}
	if l.AdapterErrors() != 101 {
		t.Fatalf("AdapterErrors = %d, want 101", l.AdapterErrors())
	}
}

// A working adapter reports nothing.
func TestLogger_WorkingAdapterReportsNothing(t *testing.T) {
	var out bytes.Buffer
	a := console.NewWithWriter(&out, jsonfmt.NewJsonFormatter())
	l := NewLogger(Config{Level: types.InfoLevel, Adapters: []types.Adapter{a}, OnAdapterError: func(name string, err error) {
		t.Errorf("unexpected failure of %s: %v", name, err)
	}})
	l.Info("m")
	l.WithField("k", 1).Info("m")
	if l.AdapterErrors() != 0 || out.Len() == 0 {
		t.Fatalf("AdapterErrors = %d, output %q", l.AdapterErrors(), out.String())
	}
}
