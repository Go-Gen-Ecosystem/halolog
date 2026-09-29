package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/asyncring"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/internal/callsite"
	"github.com/go-gen-ecosystem/halolog/types"
)

// callerShapes builds one call-site-recording logger per encode path: direct
// (a single JSON console) and capture (masking forces the structured path).
func callerShapes(extra func(*Builder)) map[string]func(*bytes.Buffer) *Logger {
	build := func(masked bool) func(*bytes.Buffer) *Logger {
		return func(buf *bytes.Buffer) *Logger {
			b := New().Component("caller").Level(types.TraceLevel).Caller().
				Adapter(console.NewWithWriter(buf, jsonfmt.NewJsonFormatter()))
			if masked {
				b = b.Masking(redactMasker{})
			}
			if extra != nil {
				extra(b)
			}
			return b.MustBuild()
		}
	}
	return map[string]func(*bytes.Buffer) *Logger{"direct": build(false), "capture": build(true)}
}

// callers decodes every JSON line in buf and returns its caller member.
func callers(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var out []string
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		c, _ := rec["caller"].(string)
		out = append(out, c)
	}
	return out
}

func site(file string, line int) string { return filepath.Base(file) + ":" + strconv.Itoa(line) }

func TestCaller_EveryAPIRecordsTheCallingLine(t *testing.T) {
	for name, build := range callerShapes(nil) {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			l := build(&buf)
			child := l.With().WithString("request_id", "req-42").Logger()
			_, file, marker, _ := runtime.Caller(0)
			l.Info("message")
			l.InfoLine().WithString("k", "v").Msg("line")
			l.InfoLine().Send()
			l.Typed().WithInt("n", 1).Info("typed")
			l.Typed().Info("typed without fields")
			l.WithField("k", "v").Info("field")
			l.WithError(nil).Error("nil error falls back to the message path")
			child.Info("child")
			child.Typed().WithInt("n", 2).Warn("child typed")
			l.Trace("trace")
			l.Debug("debug")
			l.Warn("warn")
			l.Error("error")
			got := callers(t, &buf)
			if len(got) != 13 {
				t.Fatalf("lines = %d, want 13", len(got))
			}
			for i, c := range got {
				if want := site(file, marker+1+i); c != want {
					t.Errorf("line %d: caller %q, want %q", i, c, want)
				}
			}
		})
	}
}

func TestCaller_FatalAndPanicRecordTheCallingLine(t *testing.T) {
	var buf bytes.Buffer
	exits := 0
	l := NewLogger(Config{
		Component:    "caller",
		Level:        types.InfoLevel,
		Adapters:     []types.Adapter{console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter())},
		EnableCaller: true,
		ExitFunc:     func(int) { exits++ },
	})
	_, file, marker, _ := runtime.Caller(0)
	l.Fatal("fatal")
	l.Typed().WithInt("n", 1).Fatal("typed fatal")
	var panicLine int
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Panic did not panic")
			}
		}()
		_, _, panicLine, _ = runtime.Caller(0)
		l.Panic("panic")
	}()
	want := []string{site(file, marker+1), site(file, marker+2), site(file, panicLine+1)}
	got := callers(t, &buf)
	if len(got) != len(want) || exits != 2 {
		t.Fatalf("lines=%d exits=%d, want %d lines and 2 exits", len(got), exits, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: caller %q, want %q", i, got[i], want[i])
		}
	}
}

// Both encode paths must produce the same bytes for the same line: the direct
// path copies the member the formatter renders on the capture path.
func TestCaller_DirectAndCaptureAreByteIdentical(t *testing.T) {
	shapes := callerShapes(nil)
	var direct, capture bytes.Buffer
	for _, pair := range []struct {
		l   *Logger
		buf *bytes.Buffer
	}{{shapes["direct"](&direct), &direct}, {shapes["capture"](&capture), &capture}} {
		l := pair.l
		child := l.With().WithString("request_id", "req-42").Logger()
		for i := 0; i < 2; i++ {
			l.Info("message")
			l.Typed().WithInt("status", 200).Warn("fields")
			child.Error("bound")
		}
	}
	stamp := regexp.MustCompile(`"time":"[^"]*"`)
	d, c := stamp.ReplaceAll(direct.Bytes(), nil), stamp.ReplaceAll(capture.Bytes(), nil)
	if !bytes.Equal(d, c) {
		t.Fatalf("paths differ:\ndirect:  %s\ncapture: %s", d, c)
	}
	if !bytes.Contains(d, []byte(`"caller":"caller_test.go:`)) {
		t.Fatalf("no caller member rendered: %s", d)
	}
}

func logThroughInlinedHelper(l *Logger, msg string) { l.Info(msg) }

//go:noinline
func logThroughHelper(l *Logger, msg string) { l.Info(msg) }

func TestCaller_CallerSkip(t *testing.T) {
	for name, build := range callerShapes(func(b *Builder) { b.CallerSkip(1) }) {
		var buf bytes.Buffer
		l := build(&buf)
		_, file, marker, _ := runtime.Caller(0)
		logThroughInlinedHelper(l, "through an inlined helper")
		logThroughHelper(l, "through a helper")
		got := callers(t, &buf)
		if len(got) != 2 || got[0] != site(file, marker+1) || got[1] != site(file, marker+2) {
			t.Errorf("%s: callers %v, want %q then %q", name, got, site(file, marker+1), site(file, marker+2))
		}
	}
	for _, skip := range []int{-1, maxCallerSkip + 1, math.MaxInt} {
		if _, err := New().CallerSkip(skip).Build(); err == nil {
			t.Errorf("CallerSkip(%d) built without an error", skip)
		}
	}
	for skip, want := range map[int]int{-5: 0, 3: 3, 1000: maxCallerSkip} {
		if got := NewLogger(Config{EnableCaller: true, CallerSkip: skip}).callerSkip; got != want {
			t.Errorf("Config.CallerSkip %d clamped to %d, want %d", skip, got, want)
		}
	}
}

// terminalProbe counts what a terminal line does to its adapter.
type terminalProbe struct {
	writes, flushes int
	file            string
}

func (p *terminalProbe) Name() string                  { return "terminal-probe" }
func (p *terminalProbe) Write(e *types.LogEntry) error { return p.WriteZero(e) }
func (p *terminalProbe) WriteZero(e *types.LogEntry) error {
	p.writes++
	p.file = e.File
	return nil
}
func (p *terminalProbe) Flush() error                 { p.flushes++; return nil }
func (p *terminalProbe) Close() error                 { return nil }
func (p *terminalProbe) Health() error                { return nil }
func (p *terminalProbe) SetFormatter(types.Formatter) {}

// Caller must not change what Fatal and Panic do. A root logger writes,
// flushes, and exits (or writes and panics) at every level threshold, Caller
// or not; a bound child behaves the same with Caller as without it.
func TestCaller_FatalAndPanicKeepTheirContractAtEveryThreshold(t *testing.T) {
	type outcome struct {
		writes, flushes, exits int
		panicked               bool
	}
	run := func(caller, bound bool, threshold, level types.LogLevel) (outcome, string) {
		p := &terminalProbe{}
		exits := 0
		l := NewLogger(Config{Level: threshold, Adapters: []types.Adapter{p}, EnableCaller: caller, ExitFunc: func(int) { exits++ }})
		if bound {
			l = l.With().WithString("request_id", "req-42").Logger()
		}
		var o outcome
		func() {
			defer func() { o.panicked = recover() != nil }()
			if level == types.FatalLevel {
				l.Fatal("terminal")
			} else {
				l.Panic("terminal")
			}
		}()
		o.writes, o.flushes, o.exits = p.writes, p.flushes, exits
		return o, p.file
	}
	thresholds := []types.LogLevel{types.TraceLevel, types.InfoLevel, types.ErrorLevel, types.FatalLevel, types.PanicLevel}
	for _, level := range []types.LogLevel{types.FatalLevel, types.PanicLevel} {
		for _, threshold := range thresholds {
			for _, bound := range []bool{false, true} {
				off, _ := run(false, bound, threshold, level)
				on, file := run(true, bound, threshold, level)
				if on != off {
					t.Errorf("level %d threshold %d bound=%v: with Caller %+v, without %+v", level, threshold, bound, on, off)
				}
				if bound {
					continue
				}
				want := outcome{writes: 1, flushes: 1, exits: 1}
				if level == types.PanicLevel {
					want = outcome{writes: 1, panicked: true}
				}
				if on != want {
					t.Errorf("root level %d threshold %d: %+v, want %+v", level, threshold, on, want)
				}
				if filepath.Base(file) != "caller_test.go" {
					t.Errorf("root level %d threshold %d: recorded %q, want this file", level, threshold, file)
				}
			}
		}
	}
}

func concurrentCallerLine(l *Logger, producer int) int {
	_, _, marker, _ := runtime.Caller(0)
	l.InfoLine().WithInt("producer", producer).Msg("concurrent")
	return marker + 1
}

func TestCaller_ConcurrentLinesRecordTheirSite(t *testing.T) {
	for name, build := range callerShapes(nil) {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			l := build(&out)
			wantLine := concurrentCallerLine(l, -1)
			_, file, _, _ := runtime.Caller(0)
			var wg sync.WaitGroup
			for g := 0; g < 16; g++ {
				wg.Add(1)
				go func(producer int) {
					defer wg.Done()
					for n := 0; n < 100; n++ {
						concurrentCallerLine(l, producer)
					}
				}(g)
			}
			wg.Wait()
			got := callers(t, &out)
			if len(got) != 1601 {
				t.Fatalf("lines = %d, want 1601", len(got))
			}
			for _, c := range got {
				if c != site(file, wantLine) {
					t.Fatalf("caller %q, want %q", c, site(file, wantLine))
				}
			}
		})
	}
}

// The async ring copies each entry before the pooled one is reused, so every
// record keeps the site it was logged from.
func TestCaller_AsyncRingKeepsTheSourceLocation(t *testing.T) {
	var out bytes.Buffer
	ring, err := asyncring.New(asyncring.Options{Writer: &out, Formatter: jsonfmt.NewJsonFormatter(), Capacity: 128, OnFull: asyncring.Block})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ring.Close() })
	l := New().Caller().Adapter(ring).MustBuild()
	want := map[string]string{}
	for n := 0; n < 32; n++ {
		_, file, marker, _ := runtime.Caller(0)
		l.Info(fmt.Sprintf("message-%d", n))
		l.Typed().WithInt("n", n).Info(fmt.Sprintf("typed-%d", n))
		want[fmt.Sprintf("message-%d", n)] = site(file, marker+1)
		want[fmt.Sprintf("typed-%d", n)] = site(file, marker+2)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	count := 0
	for {
		var rec map[string]any
		if err := d.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		msg, _ := rec["message"].(string)
		if rec["caller"] != want[msg] {
			t.Fatalf("record %q has caller %v, want %q", msg, rec["caller"], want[msg])
		}
		count++
	}
	if count != 64 || ring.Dropped() != 0 {
		t.Fatalf("records=%d dropped=%d, want 64 and 0", count, ring.Dropped())
	}
}

// jsonWithoutDirect wraps the JSON formatter without exposing its direct
// encoder, forcing the capture path while keeping the same output.
type jsonWithoutDirect struct{ f *jsonfmt.Formatter }

func (w *jsonWithoutDirect) Format(e *types.LogEntry, dst []byte) []byte { return w.f.Format(e, dst) }
func (w *jsonWithoutDirect) EstimatedSize() int                          { return w.f.EstimatedSize() }
func (w *jsonWithoutDirect) Reset()                                      { w.f.Reset() }

// A formatter swapped at runtime moves lines between the direct and capture
// paths; the caller and the bound context must survive both moves.
func TestCaller_RuntimeFormatterSwap(t *testing.T) {
	var out bytes.Buffer
	adapter := console.NewWithWriter(&out, jsonfmt.NewJsonFormatter())
	l := New().Caller().Adapter(adapter).MustBuild().With().WithString("request_id", "req-42").Logger()
	for _, f := range []types.Formatter{&jsonWithoutDirect{f: jsonfmt.NewJsonFormatter()}, jsonfmt.NewJsonFormatter()} {
		adapter.SetFormatter(f)
		out.Reset()
		_, file, marker, _ := runtime.Caller(0)
		l.Info("after a swap")
		if got := callers(t, &out); len(got) != 1 || got[0] != site(file, marker+1) {
			t.Fatalf("formatter %T: callers %v, want %q", f, got, site(file, marker+1))
		}
		if !bytes.Contains(out.Bytes(), []byte(`"request_id":"req-42"`)) {
			t.Fatalf("formatter %T: bound context lost: %s", f, out.Bytes())
		}
	}
}

func TestCaller_OffByDefaultAndOnDiscard(t *testing.T) {
	var buf bytes.Buffer
	l := New().Adapter(console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter())).MustBuild()
	l.Info("message")
	l.Typed().WithInt("n", 1).Info("typed")
	for _, c := range callers(t, &buf) {
		if c != "" {
			t.Fatalf("caller recorded without Caller(): %q", c)
		}
	}
	if New().Discard().Caller().MustBuild().recordCaller {
		t.Fatal("a discarding logger must not walk the stack")
	}
}

// Filtered and sampled-out lines return before the stack walk.
func TestCaller_DroppedLinesDoNotWalk(t *testing.T) {
	var buf bytes.Buffer
	warn := New().Level(types.WarnLevel).Caller().
		Adapter(console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter())).MustBuild()
	sampled := NewLogger(Config{
		Level:          types.InfoLevel,
		Adapters:       []types.Adapter{console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter())},
		EnableSampling: true,
		Sampler:        &fixedSampler{sample: false},
		EnableCaller:   true,
	})
	before := callsite.Retained()
	warn.Info("filtered")
	warn.InfoLine().WithInt("n", 1).Msg("filtered")
	warn.Typed().WithInt("n", 1).Debug("filtered")
	sampled.Info("sampled out")
	sampled.Typed().WithInt("n", 1).Info("sampled out")
	if after := callsite.Retained(); after != before || buf.Len() != 0 {
		t.Fatalf("dropped lines resolved sites (%d -> %d) or wrote %q", before, after, buf.Bytes())
	}
}

// The capture path clears File/Line before the pooled entry goes back, so a
// logger without Caller never renders a stale site.
func TestCaller_PooledEntryDoesNotCarryTheSite(t *testing.T) {
	var withCaller, without bytes.Buffer
	a := New().Caller().Masking(redactMasker{}).
		Adapter(console.NewWithWriter(&withCaller, jsonfmt.NewJsonFormatter())).MustBuild()
	b := New().Masking(redactMasker{}).
		Adapter(console.NewWithWriter(&without, jsonfmt.NewJsonFormatter())).MustBuild()
	for i := 0; i < 100; i++ {
		a.Typed().WithInt("n", i).Info("with caller")
		b.Typed().WithInt("n", i).Info("without caller")
		b.Info("without caller")
	}
	for _, c := range callers(t, &without) {
		if c != "" {
			t.Fatalf("stale call site leaked into a logger without Caller: %q", c)
		}
	}
}

func TestCaller_UnknownFrameRendersNothing(t *testing.T) {
	if m := callerMember(callsite.Lookup(0)); m != nil {
		t.Fatalf("an unplaceable frame must render nothing, got %q", m)
	}
}

// BenchmarkCaller measures lines with call-site capture against the same
// lines without it, on both encode paths (a JSON console writing to
// io.Discard; masking forces the capture path).
func BenchmarkCaller(b *testing.B) {
	for _, path := range []string{"direct", "capture"} {
		for _, mode := range []string{"off", "on"} {
			build := New().Adapter(console.NewWithWriter(io.Discard, jsonfmt.NewJsonFormatter()))
			if path == "capture" {
				build = build.Masking(redactMasker{})
			}
			if mode == "on" {
				build = build.Caller()
			}
			l := build.MustBuild()
			b.Run(path+"/"+mode+"/Info", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					l.Info("handled")
				}
			})
			b.Run(path+"/"+mode+"/Typed", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					l.Typed().WithInt("status", 200).Info("handled")
				}
			})
		}
	}
}
