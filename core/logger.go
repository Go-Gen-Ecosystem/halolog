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

// Package core provides the high-performance HaloLog logger implementation.
// Author: Admilson B. F. Cossa

package core

import (
	"errors"
	"os"
	"sync/atomic"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/discard"
	"github.com/go-gen-ecosystem/halolog/cache"
	"github.com/go-gen-ecosystem/halolog/pool"
	"github.com/go-gen-ecosystem/halolog/types"
)

// hotState groups the data every log call touches behind one atomic pointer.
// All log dispatch happens through function pointers here.
// Size: 2×8 bytes of scalars + 7×8 bytes of function pointers + 8 bytes of
// padding = 80 bytes (one 64-byte cache line plus the spill; the pointers used
// by a single call still land on the first line).
type hotState struct {
	// Cached timestamp from clock
	cachedNanos uint64 // 8 bytes

	// Level threshold
	level uint64 // 8 bytes

	// Function pointers for level-based dispatch
	// Set at startup based on configuration - avoids method value escapes
	infoFunc  func(*Logger, string)
	debugFunc func(*Logger, string)
	warnFunc  func(*Logger, string)
	errorFunc func(*Logger, string)
	fatalFunc func(*Logger, string)
	panicFunc func(*Logger, string)
	traceFunc func(*Logger, string)

	_ [8]byte // Round the struct up to a multiple of 8 words
}

// Logger is the core logging engine.
// Uses the clean package's proven approach: function pointers in hot state,
// concrete adapter types for discard optimization, stack-allocated entries.
type Logger struct {
	// Hot state pointer (cache line 1)
	hot atomic.Pointer[hotState]
	_   [56]byte // Pad to 64 bytes

	// Cold state
	component      string
	clock          *cache.CachedClock
	adapters       []types.Adapter
	masker         types.PIIMasker
	enableMasking  bool
	discardAdapter *discard.Adapter // Concrete type for zero-allocation

	// Optional features (nil if not configured)
	sampler types.Sampler
	metrics *metricsCollector

	// exitFunc terminates the process after a Fatal line (default os.Exit).
	// Overridable via Config.ExitFunc for tests and embedders.
	exitFunc func(int)

	// Call-site capture (see caller.go). recordCaller is set only when
	// Config.EnableCaller asks for it and output is not discarded; callerSkip
	// counts wrapper frames between the application's call and HaloLog.
	recordCaller bool
	callerSkip   int

	// Bound context (child loggers, see context.go). Both are immutable after
	// construction and empty on root loggers. boundBytes is the context
	// pre-encoded once as `,"k":v…` — the direct path emits it with a single
	// memcpy per line; boundFields is the same context as structured data,
	// prepended at state acquisition on the capture path so maskers see it
	// and both paths stay byte-identical.
	boundFields []types.TypedFieldData
	boundBytes  []byte

	// Direct-append fast path (both non-nil only when the sole adapter accepts
	// raw lines and can expose a direct encoder, and no per-entry transform
	// such as masking or sampling is configured — see NewLogger). The encoder
	// itself is re-queried per line via directAdapter so a formatter swap
	// safely disables the path. SECURITY INVARIANT: masking configured ⇒ this
	// stays nil, so the fast path can never bypass PII masking.
	rawWriter     types.RawWriter
	directAdapter types.DirectCapableAdapter
}

// metricsCollector tracks logging metrics.
type metricsCollector struct {
	counts [8]atomic.Int64
}

// Config holds logger configuration.
type Config struct {
	Component string
	Level     types.LogLevel
	Adapters  []types.Adapter

	// Optional features
	EnableMasking  bool
	Masker         types.PIIMasker
	EnableSampling bool
	Sampler        types.Sampler
	EnableMetrics  bool

	// ExitFunc replaces os.Exit for Fatal-level lines. Leave nil for the
	// conventional behavior (log, flush, os.Exit(1)); set it in tests or in
	// hosts that must intercept termination.
	ExitFunc func(int)

	// EnableCaller records where each line was logged, as the member
	// "caller":"file.go:42". It adds one stack walk per line; loggers without
	// it do no walk. Once a call site has logged, its lines allocate nothing,
	// for up to 4096 call sites per process.
	EnableCaller bool

	// CallerSkip moves the recorded call site that many frames up, for
	// applications that log through their own helper functions. Values are
	// clamped to [0, 64].
	CallerSkip int

	// Advanced features
	EnableAlerts      bool
	EnableAggregation bool
}

// NewLogger creates a new logger with the given configuration.
func NewLogger(config Config) *Logger {
	l := &Logger{
		component:     config.Component,
		clock:         cache.GetGlobalCachedClock(),
		adapters:      config.Adapters,
		masker:        config.Masker,
		enableMasking: config.EnableMasking && config.Masker != nil,
		exitFunc:      config.ExitFunc,
	}

	// Detect discard adapter for zero-allocation optimization
	if len(config.Adapters) == 1 {
		if da, ok := config.Adapters[0].(*discard.Adapter); ok {
			l.discardAdapter = da
		}
	}

	// Recording call sites for discarded lines would be pure cost.
	l.recordCaller = config.EnableCaller && l.discardAdapter == nil
	l.callerSkip = clampCallerSkip(config.CallerSkip)

	// Set up optional features
	if config.EnableSampling && config.Sampler != nil {
		l.sampler = config.Sampler
	}

	if config.EnableMetrics {
		l.metrics = &metricsCollector{}
	}

	// Direct-append eligibility: exactly one non-discard adapter that accepts
	// raw lines and can expose a direct encoder, with no per-entry transform
	// (masking, sampling) configured. Anything else keeps the capture path.
	if len(config.Adapters) == 1 && l.discardAdapter == nil &&
		!l.enableMasking && l.sampler == nil {
		if rw, ok := config.Adapters[0].(types.RawWriter); ok {
			if dc, ok := config.Adapters[0].(types.DirectCapableAdapter); ok {
				l.rawWriter = rw
				l.directAdapter = dc
			}
		}
	}

	// Build hot state
	hot := &hotState{
		level:       uint64(config.Level),
		cachedNanos: uint64(l.clock.GetNsecValue()),
	}

	// Set up function pointers for optimized dispatch
	l.setupFunctionPointers(hot, config.Level)

	// Store hot state
	l.hot.Store(hot)

	return l
}

// setupFunctionPointers sets up function pointers based on configuration.
// This is the startup-time specialization - each level gets the exact function it needs.
func (l *Logger) setupFunctionPointers(hot *hotState, level types.LogLevel) {
	// Call-site capture: every level takes the pooled route, whose single
	// dispatch point records the caller and still applies bound context,
	// sampling, masking, metrics, and Fatal/Panic semantics. Loggers without
	// it keep the specialised paths below, untouched.
	if l.recordCaller {
		hot.traceFunc = noopLog
		if level <= types.TraceLevel {
			hot.traceFunc = traceCaller
		}
		hot.debugFunc = noopLog
		if level <= types.DebugLevel {
			hot.debugFunc = debugCaller
		}
		hot.infoFunc = noopLog
		if level <= types.InfoLevel {
			hot.infoFunc = infoCaller
		}
		hot.warnFunc = noopLog
		if level <= types.WarnLevel {
			hot.warnFunc = warnCaller
		}
		hot.errorFunc = noopLog
		if level <= types.ErrorLevel {
			hot.errorFunc = errorCaller
		}
		// Fatal and Panic keep the contract they have without Caller: a root
		// logger writes, flushes, and exits (or panics) at every threshold, and
		// a bound child takes the pooled route that renders its context.
		hot.fatalFunc = l.realFatal
		hot.panicFunc = l.realPanic
		if len(l.boundFields) > 0 {
			hot.fatalFunc = fatalCaller
			hot.panicFunc = panicCaller
		}
		return
	}

	// Bound context on a non-direct, non-discard logger: route every level
	// through the pooled capture path, whose single dispatch point
	// (dispatchLine) already handles the bound prefix, sampling, masking,
	// metrics, and Fatal/Panic semantics — one implementation, not a
	// duplicated matrix. Direct-eligible bound loggers skip this gate: their
	// message path appends the pre-encoded bound bytes itself (logDirect),
	// and discard drops everything regardless.
	if len(l.boundFields) > 0 &&
		(l.rawWriter == nil || l.directAdapter == nil) &&
		l.discardAdapter == nil {
		hot.traceFunc = noopLog
		if level <= types.TraceLevel {
			hot.traceFunc = l.traceBound
		}
		hot.debugFunc = noopLog
		if level <= types.DebugLevel {
			hot.debugFunc = l.debugBound
		}
		hot.infoFunc = noopLog
		if level <= types.InfoLevel {
			hot.infoFunc = l.infoBound
		}
		hot.warnFunc = noopLog
		if level <= types.WarnLevel {
			hot.warnFunc = l.warnBound
		}
		hot.errorFunc = noopLog
		if level <= types.ErrorLevel {
			hot.errorFunc = l.errorBound
		}
		hot.fatalFunc = l.fatalBound
		hot.panicFunc = l.panicBound
		return
	}

	// Sampling configured: route every samplable level through the
	// sampler-aware generic path. Sampling implies extra per-line work anyway,
	// so the mask×adapter specialization matrix is not duplicated for it.
	// Fatal and Panic are never sampled (see logSampled) and keep their funcs.
	if l.sampler != nil {
		hot.traceFunc = noopLog
		if level <= types.TraceLevel {
			hot.traceFunc = l.traceSampled
		}
		hot.debugFunc = noopLog
		if level <= types.DebugLevel {
			hot.debugFunc = l.debugSampled
		}
		hot.infoFunc = noopLog
		if level <= types.InfoLevel {
			hot.infoFunc = l.infoSampled
		}
		hot.warnFunc = noopLog
		if level <= types.WarnLevel {
			hot.warnFunc = l.warnSampled
		}
		hot.errorFunc = noopLog
		if level <= types.ErrorLevel {
			hot.errorFunc = l.errorSampled
		}
		hot.fatalFunc = l.realFatal
		hot.panicFunc = l.realPanic
		return
	}

	// Direct-append eligibility (single raw-capable JSON adapter, no masking,
	// no sampling — see NewLogger): message-only lines render straight to
	// bytes through the same fused-header path the typed builders use, so
	// l.Info(msg) never touches a LogEntry at all. Fatal/Panic keep the
	// generic funcs for their flush/exit/panic semantics.
	if l.rawWriter != nil && l.directAdapter != nil {
		hot.traceFunc = noopLog
		if level <= types.TraceLevel {
			hot.traceFunc = l.traceDirect
		}
		hot.debugFunc = noopLog
		if level <= types.DebugLevel {
			hot.debugFunc = l.debugDirect
		}
		hot.infoFunc = noopLog
		if level <= types.InfoLevel {
			hot.infoFunc = l.infoDirect
		}
		hot.warnFunc = noopLog
		if level <= types.WarnLevel {
			hot.warnFunc = l.warnDirect
		}
		hot.errorFunc = noopLog
		if level <= types.ErrorLevel {
			hot.errorFunc = l.errorDirect
		}
		hot.fatalFunc = l.realFatal
		hot.panicFunc = l.realPanic
		if len(l.boundFields) > 0 {
			// Fatal/Panic must carry the bound context too; the pooled
			// capture route renders it (byte-identical) and dispatchLine
			// applies the flush-exit / panic contract.
			hot.fatalFunc = l.fatalBound
			hot.panicFunc = l.panicBound
		}
		return
	}

	// Discard adapter optimization
	if l.discardAdapter != nil {
		// Use concrete discard adapter - zero interface dispatch
		if level <= types.TraceLevel {
			hot.traceFunc = l.realTraceDiscard
		} else {
			hot.traceFunc = noopLog
		}
		if level <= types.DebugLevel {
			hot.debugFunc = l.realDebugDiscard
		} else {
			hot.debugFunc = noopLog
		}
		if level <= types.InfoLevel {
			hot.infoFunc = l.realInfoDiscard
		} else {
			hot.infoFunc = noopLog
		}
		if level <= types.WarnLevel {
			hot.warnFunc = l.realWarnDiscard
		} else {
			hot.warnFunc = noopLog
		}
		if level <= types.ErrorLevel {
			hot.errorFunc = l.realErrorDiscard
		} else {
			hot.errorFunc = noopLog
		}
		hot.fatalFunc = l.realFatalDiscard
		hot.panicFunc = l.realPanicDiscard
		return
	}

	// Regular adapter path - Specialize EVERYTHING
	// We select the EXACT function needed to avoid "if masking" or "if loop" checks at runtime.

	// Pre-calculate flags
	isMasking := l.enableMasking
	isSingle := len(l.adapters) == 1

	// Setup Info
	if level <= types.InfoLevel {
		if isMasking {
			if isSingle {
				hot.infoFunc = l.infoMaskOne
			} else {
				hot.infoFunc = l.infoMaskMulti
			}
		} else {
			if isSingle {
				hot.infoFunc = l.infoNoMaskOne
			} else {
				hot.infoFunc = l.infoNoMaskMulti
			}
		}
	} else {
		hot.infoFunc = noopLog
	}

	// Setup Debug
	if level <= types.DebugLevel {
		if isMasking {
			if isSingle {
				hot.debugFunc = l.debugMaskOne
			} else {
				hot.debugFunc = l.debugMaskMulti
			}
		} else {
			if isSingle {
				hot.debugFunc = l.debugNoMaskOne
			} else {
				hot.debugFunc = l.debugNoMaskMulti
			}
		}
	} else {
		hot.debugFunc = noopLog
	}

	// Setup Warn
	if level <= types.WarnLevel {
		if isMasking {
			if isSingle {
				hot.warnFunc = l.warnMaskOne
			} else {
				hot.warnFunc = l.warnMaskMulti
			}
		} else {
			if isSingle {
				hot.warnFunc = l.warnNoMaskOne
			} else {
				hot.warnFunc = l.warnNoMaskMulti
			}
		}
	} else {
		hot.warnFunc = noopLog
	}

	// Setup Error
	if level <= types.ErrorLevel {
		if isMasking {
			if isSingle {
				hot.errorFunc = l.errorMaskOne
			} else {
				hot.errorFunc = l.errorMaskMulti
			}
		} else {
			if isSingle {
				hot.errorFunc = l.errorNoMaskOne
			} else {
				hot.errorFunc = l.errorNoMaskMulti
			}
		}
	} else {
		hot.errorFunc = noopLog
	}

	// Setup Trace (using generic realTrace for now, optimize if needed)
	if level <= types.TraceLevel {
		hot.traceFunc = l.realTrace
	} else {
		hot.traceFunc = noopLog
	}

	// Fatal/Panic (less critical)
	hot.fatalFunc = l.realFatal
	hot.panicFunc = l.realPanic
}

// noopLog is the no-op function for disabled levels.
func noopLog(_ *Logger, _ string) {}

// ===== HOT PATH METHODS =====

// Trace logs a trace message.
func (l *Logger) Trace(msg string) {
	hot := l.hot.Load()
	hot.traceFunc(l, msg)
}

// Debug logs a debug message.
func (l *Logger) Debug(msg string) {
	hot := l.hot.Load()
	hot.debugFunc(l, msg)
}

// Info logs an info message.
func (l *Logger) Info(msg string) {
	hot := l.hot.Load()
	hot.infoFunc(l, msg)
}

// Warn logs a warning message.
func (l *Logger) Warn(msg string) {
	hot := l.hot.Load()
	hot.warnFunc(l, msg)
}

// Error logs an error message.
func (l *Logger) Error(msg string) {
	hot := l.hot.Load()
	hot.errorFunc(l, msg)
}

// Fatal logs a fatal message.
func (l *Logger) Fatal(msg string) {
	hot := l.hot.Load()
	hot.fatalFunc(l, msg)
}

// Panic logs a panic message.
func (l *Logger) Panic(msg string) {
	hot := l.hot.Load()
	hot.panicFunc(l, msg)
}

// ===== DISCARD-OPTIMIZED FUNCTIONS (Concrete type dispatch) =====
//
// The discard adapter ignores its argument entirely, so every level passes
// nil and skips entry construction. This is deliberately SYMMETRIC across
// levels: previously only Info took the shortcut, which quietly made the one
// level every benchmark measures cheaper than its siblings.

func (l *Logger) realTraceDiscard(_ *Logger, _ string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.TraceLevel].Add(1)
	}
}

func (l *Logger) realDebugDiscard(_ *Logger, _ string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.DebugLevel].Add(1)
	}
}

func (l *Logger) realInfoDiscard(_ *Logger, _ string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.InfoLevel].Add(1)
	}
}

func (l *Logger) realWarnDiscard(_ *Logger, _ string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.WarnLevel].Add(1)
	}
}

func (l *Logger) realErrorDiscard(_ *Logger, _ string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.ErrorLevel].Add(1)
	}
}

func (l *Logger) realFatalDiscard(_ *Logger, msg string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.FatalLevel].Add(1)
	}
	l.exit(1)
}

func (l *Logger) realPanicDiscard(_ *Logger, msg string) {
	_ = l.discardAdapter.WriteZero(nil)
	if l.metrics != nil {
		l.metrics.counts[types.PanicLevel].Add(1)
	}
	panic(msg)
}

// ===== REGULAR ADAPTER FUNCTIONS =====

// writeEntry masks (when configured) and writes one entry to all adapters,
// then bumps the level's metric. Shared by the generic level funcs.
func (l *Logger) writeEntry(entry *types.LogEntry) {
	if l.enableMasking {
		l.masker.Apply(entry)
	}

	if len(l.adapters) == 1 {
		_ = l.adapters[0].WriteZero(entry)
	} else {
		for _, a := range l.adapters {
			_ = a.WriteZero(entry)
		}
	}

	if l.metrics != nil {
		l.metrics.counts[entry.Level].Add(1)
	}
}

func (l *Logger) realTrace(_ *Logger, msg string) {
	entry := pool.AcquireEntry()
	entry.Level = types.TraceLevel
	entry.Message = msg
	entry.Component = l.component
	entry.TimestampUnix = l.clock.GetNsecValue()
	l.writeEntry(entry)
	pool.ReleaseEntry(entry)
}

// realFatal writes the fatal line, flushes every adapter so the line is not
// lost in a buffer, and terminates the process (conventional Fatal semantics —
// zap, zerolog, logrus, and the standard library all exit here).
func (l *Logger) realFatal(_ *Logger, msg string) {
	var entry types.LogEntry
	entry.Level = types.FatalLevel
	entry.Message = msg
	entry.Component = l.component
	entry.TimestampUnix = l.clock.GetNsecValue()
	entry.StaticFieldCount = 0
	if l.recordCaller {
		site := l.callSiteAt(terminalCallerFrames) // Logger.Fatal, then its caller
		entry.File, entry.Line = site.File, site.Line
	}
	l.writeEntry(&entry)
	_ = l.Flush()
	l.exit(1)
}

// realPanic writes the panic line, then panics with the message (conventional
// Panic semantics).
func (l *Logger) realPanic(_ *Logger, msg string) {
	var entry types.LogEntry
	entry.Level = types.PanicLevel
	entry.Message = msg
	entry.Component = l.component
	entry.TimestampUnix = l.clock.GetNsecValue()
	entry.StaticFieldCount = 0
	if l.recordCaller {
		site := l.callSiteAt(terminalCallerFrames) // Logger.Panic, then its caller
		entry.File, entry.Line = site.File, site.Line
	}
	l.writeEntry(&entry)
	panic(msg)
}

// exit terminates the process after a fatal line, honoring Config.ExitFunc.
func (l *Logger) exit(code int) {
	if l.exitFunc != nil {
		l.exitFunc(code)
		return
	}
	os.Exit(code)
}

// ===== DIRECT MESSAGE-ONLY FUNCTIONS (single raw-capable JSON adapter) =====

// logDirect renders a message-only line straight to bytes: fused header +
// closer into the pooled line buffer, one WriteRaw. No LogEntry is built.
// The encoder is re-queried per line (a lock-free atomic load on the console
// adapter), so a formatter without a direct JSON encoder (a text or custom
// formatter, or a runtime swap to one) takes the pooled capture path instead:
// captureState prepends any bound context and dispatchLine writes the line.
func (l *Logger) logDirect(level types.LogLevel, msg string) {
	enc, _ := l.directAdapter.DirectEncoder().(*jsonfmt.Formatter)
	if enc == nil {
		s := captureState(l)
		dispatchLine(l, s, s.epoch, level, msg, messageCallerFrames)
		return
	}
	s := globalPerPPool.get()
	line := enc.AppendHeader(s.lineBuf[:0], l.clock.GetNsecValue(), level, msg)
	if len(l.boundBytes) > 0 {
		line = append(line, l.boundBytes...) // whole bound context: one memcpy
	}
	line = jsonfmt.AppendCloser(line)
	s.lineBuf = line // retain growth for reuse
	_ = l.rawWriter.WriteRaw(line)
	if l.metrics != nil {
		l.metrics.counts[level].Add(1)
	}
	globalPerPPool.put(s)
}

func (l *Logger) traceDirect(_ *Logger, msg string) { l.logDirect(types.TraceLevel, msg) }
func (l *Logger) debugDirect(_ *Logger, msg string) { l.logDirect(types.DebugLevel, msg) }
func (l *Logger) infoDirect(_ *Logger, msg string)  { l.logDirect(types.InfoLevel, msg) }
func (l *Logger) warnDirect(_ *Logger, msg string)  { l.logDirect(types.WarnLevel, msg) }
func (l *Logger) errorDirect(_ *Logger, msg string) { l.logDirect(types.ErrorLevel, msg) }

// ===== BOUND-CONTEXT MESSAGE FUNCTIONS (child loggers on the capture path) =====

// logBoundCapture routes a message-only line on a bound (child) logger
// through the pooled capture path: captureState prepends the bound fields,
// and dispatchLine — the single dispatch point — applies sampling, masking,
// metrics, and terminal semantics. No logic is duplicated here.
func (l *Logger) logBoundCapture(level types.LogLevel, msg string) {
	s := captureState(l)
	dispatchLine(l, s, s.epoch, level, msg, messageCallerFrames)
}

func (l *Logger) traceBound(_ *Logger, msg string) { l.logBoundCapture(types.TraceLevel, msg) }
func (l *Logger) debugBound(_ *Logger, msg string) { l.logBoundCapture(types.DebugLevel, msg) }
func (l *Logger) infoBound(_ *Logger, msg string)  { l.logBoundCapture(types.InfoLevel, msg) }
func (l *Logger) warnBound(_ *Logger, msg string)  { l.logBoundCapture(types.WarnLevel, msg) }
func (l *Logger) errorBound(_ *Logger, msg string) { l.logBoundCapture(types.ErrorLevel, msg) }
func (l *Logger) fatalBound(_ *Logger, msg string) { l.logBoundCapture(types.FatalLevel, msg) }
func (l *Logger) panicBound(_ *Logger, msg string) { l.logBoundCapture(types.PanicLevel, msg) }

// ===== SAMPLED LEVEL FUNCTIONS (selected when a sampler is configured) =====

// logSampled is the generic dispatch used when sampling is on: build the
// entry, consult the sampler, then write. Fatal/Panic never route here.
func (l *Logger) logSampled(level types.LogLevel, msg string) {
	entry := pool.AcquireEntry()
	entry.Level = level
	entry.Message = msg
	entry.Component = l.component
	entry.TimestampUnix = l.clock.GetNsecValue()

	if l.sampler.ShouldSample(entry) {
		l.writeEntry(entry)
	}
	pool.ReleaseEntry(entry)
}

func (l *Logger) traceSampled(_ *Logger, msg string) { l.logSampled(types.TraceLevel, msg) }
func (l *Logger) debugSampled(_ *Logger, msg string) { l.logSampled(types.DebugLevel, msg) }
func (l *Logger) infoSampled(_ *Logger, msg string)  { l.logSampled(types.InfoLevel, msg) }
func (l *Logger) warnSampled(_ *Logger, msg string)  { l.logSampled(types.WarnLevel, msg) }
func (l *Logger) errorSampled(_ *Logger, msg string) { l.logSampled(types.ErrorLevel, msg) }

// WithField returns a fluent builder for adding fields.
// Returns by VALUE to avoid heap escape.
// Gets per-P state lazily on first field addition.
func (l *Logger) WithField(key string, value interface{}) FieldBuilder {
	state := captureState(l)
	n := state.entry.StaticFieldCount // bound-context prefix, if any
	state.entry.StaticFields[n] = types.TypedFieldData{Key: key, Value: value}
	state.entry.StaticFieldCount = n + 1

	return FieldBuilder{
		logger: l,
		state:  state,
		epoch:  state.epoch,
	}
}

// WithError returns a fluent builder with an error field.
// Returns by VALUE to avoid heap escape.
func (l *Logger) WithError(err error) FieldBuilder {
	if err == nil {
		return FieldBuilder{logger: l}
	}

	state := captureState(l)
	n := state.entry.StaticFieldCount // bound-context prefix, if any
	state.entry.StaticFields[n] = types.TypedFieldData{Key: "error", Value: err.Error()}
	state.entry.StaticFieldCount = n + 1

	return FieldBuilder{
		logger: l,
		state:  state,
		epoch:  state.epoch,
	}
}

// Component returns the logger's component name.
func (l *Logger) Component() string {
	return l.component
}

// Level returns the logger's minimum log level.
func (l *Logger) Level() types.LogLevel {
	hot := l.hot.Load()
	return types.LogLevel(hot.level)
}

// Flush flushes all adapters. Every adapter is flushed even when an earlier
// one fails; the failures are joined into one error.
func (l *Logger) Flush() error {
	var errs []error
	for _, a := range l.adapters {
		if err := a.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes all adapters and releases resources. Every adapter is closed
// even when an earlier one fails; the failures are joined into one error.
func (l *Logger) Close() error {
	var errs []error
	for _, a := range l.adapters {
		if err := a.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
