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
	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/types"
)

// TypedFieldBuilder provides a typed fluent API that avoids interface{} boxing.
// Use it when field types are known at compile time.
//
// Usage:
//
//	logger.Typed().WithString("user", "alice").WithInt("count", 42).Info("message")
//
// Like FieldBuilder it is a VALUE TYPE, but it holds only two pointers (logger
// and the pooled per-P state), so chaining copies almost nothing. Fields are
// written straight into the pooled entry as boxing-free FieldValues, so the
// serializer can render them without an interface type switch. Contrast with the
// generic FieldBuilder.WithField, which stores an interface{} value.
type TypedFieldBuilder struct {
	logger *Logger
	state  *perPState // per-P pooled state (acquired on first field)
	epoch  uint32     // state generation captured at acquisition
}

// Typed returns a typed field builder for boxing-free structured logging.
func (l *Logger) Typed() TypedFieldBuilder {
	return TypedFieldBuilder{logger: l}
}

// acquire fetches the pooled per-P state for a new line and selects the line's
// encoding mode: when the logger is direct-eligible, the adapter's current
// direct encoder is captured once here, and every field of this line is encoded
// straight to bytes instead of being captured as a struct. Querying per line
// (not per logger) means a formatter swap safely flips subsequent lines back to
// the capture path.
func (fb TypedFieldBuilder) acquire() TypedFieldBuilder {
	fb.state = acquireState(fb.logger)
	fb.epoch = fb.state.epoch
	return fb
}

// acquireState fetches the pooled per-P state for a new line and selects its
// encoding mode: the adapter's current direct encoder is asserted to the
// CONCRETE JSON formatter so every subsequent per-field call is statically
// dispatched (jsonfmt package functions — no interface calls on the hot loop).
// Querying per line means a runtime formatter swap safely flips later lines
// back to the capture path; a non-JSON encoder also keeps the capture path.
func acquireState(l *Logger) *perPState {
	s := globalPerPPool.get()
	if da := l.directAdapter; da != nil {
		s.directJSON, _ = da.DirectEncoder().(*jsonfmt.Formatter)
	}
	if s.directJSON == nil {
		prefillBound(s, l)
	}
	return s
}

// captureState fetches pooled state for a line that must take the capture
// path regardless of adapter capabilities (the interface{}-valued
// FieldBuilder API, and bound message-only lines). The bound-context prefix
// is prepended here — the one place capture-path acquisition happens.
func captureState(l *Logger) *perPState {
	s := globalPerPPool.get()
	prefillBound(s, l)
	return s
}

// prefillBound copies the logger's bound context into the entry's static
// prefix so the capture path renders (and masks) it ahead of per-line fields
// — mirroring the direct path, which emits the same context from its
// pre-encoded bytes. Bound size is capped at construction (maxBoundFields),
// so the copy always fits the 64-slot buffer.
func prefillBound(s *perPState, l *Logger) {
	if len(l.boundFields) == 0 {
		return
	}
	n := copy(s.entry.StaticFields, l.boundFields)
	s.entry.StaticFieldCount = n
}

// captureField stores one field as a TypedFieldData for the formatter — the
// capture path used when the line cannot direct-encode (multiple adapters,
// masking, a non-JSON formatter). Allocation-free up to the 64-slot buffer.
func captureField(s *perPState, kd *types.FieldKey, key string, val types.FieldValue) {
	entry := &s.entry
	n := entry.StaticFieldCount
	if n < len(entry.StaticFields) {
		entry.StaticFields[n] = types.TypedFieldData{Key: key, KeyDesc: kd, Val: val}
		entry.StaticFieldCount = n + 1
	} else {
		entry.Fields = append(entry.Fields, types.TypedFieldData{Key: key, KeyDesc: kd, Val: val})
	}
}

// addField writes one generic FieldValue into the line (kd nil ⇒ plain string
// key). Kept for Any-kind values; the known-type setters below use the
// per-type helpers instead, which skip the FieldValue box entirely on the
// direct path — profiling showed the box and its call funnel costing more
// than the actual byte encoding on field-heavy lines.
func addField(s *perPState, kd *types.FieldKey, key string, val types.FieldValue) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendField(s.directFields, kd, key, val)
		return
	}
	captureField(s, kd, key, val)
}

// Per-type field writers, shared by the typed builder and the Line API. On the
// direct fast path each appends `,"key":value` bytes immediately (the
// zerolog/phuslu model — no intermediate struct); on the capture path the
// FieldValue box is built only then, where it is actually needed.

func addStr(s *perPState, kd *types.FieldKey, key, value string) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendStringField(s.directFields, kd, key, value)
		return
	}
	captureField(s, kd, key, types.StringValue(value))
}

func addInt(s *perPState, kd *types.FieldKey, key string, value int) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendIntField(s.directFields, kd, key, int64(value))
		return
	}
	captureField(s, kd, key, types.IntValue(value))
}

func addInt64(s *perPState, kd *types.FieldKey, key string, value int64) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendIntField(s.directFields, kd, key, value)
		return
	}
	captureField(s, kd, key, types.Int64Value(value))
}

func addFloat64(s *perPState, kd *types.FieldKey, key string, value float64) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendFloat64Field(s.directFields, kd, key, value)
		return
	}
	captureField(s, kd, key, types.Float64Value(value))
}

func addBool(s *perPState, kd *types.FieldKey, key string, value bool) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendBoolField(s.directFields, kd, key, value)
		return
	}
	captureField(s, kd, key, types.BoolValue(value))
}

// addErr renders exactly like a string field of err.Error() on the direct
// path (KindError and KindString encode identically) while the capture path
// keeps the Error kind for maskers and adapters. err must be non-nil.
func addErr(s *perPState, kd *types.FieldKey, key string, err error) {
	if s.directJSON != nil {
		s.directFields = jsonfmt.AppendStringField(s.directFields, kd, key, err.Error())
		return
	}
	captureField(s, kd, key, types.ErrorValue(err))
}

// WithString adds a string field without interface{} boxing.
func (fb TypedFieldBuilder) WithString(key string, value string) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addStr(fb.state, nil, key, value)
	return fb
}

// WithInt adds an int field without interface{} boxing.
func (fb TypedFieldBuilder) WithInt(key string, value int) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addInt(fb.state, nil, key, value)
	return fb
}

// WithInt64 adds an int64 field without interface{} boxing.
func (fb TypedFieldBuilder) WithInt64(key string, value int64) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addInt64(fb.state, nil, key, value)
	return fb
}

// WithFloat64 adds a float64 field without interface{} boxing.
func (fb TypedFieldBuilder) WithFloat64(key string, value float64) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addFloat64(fb.state, nil, key, value)
	return fb
}

// WithBool adds a bool field without interface{} boxing.
func (fb TypedFieldBuilder) WithBool(key string, value bool) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addBool(fb.state, nil, key, value)
	return fb
}

// WithAny adds an arbitrary value under a plain string key. Prefer the typed
// setters on hot paths — Any values box through an interface.
func (fb TypedFieldBuilder) WithAny(key string, value interface{}) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addField(fb.state, nil, key, types.AnyValue(value))
	return fb
}

// WithError adds an error field without interface{} boxing. A nil error is a
// no-op.
func (fb TypedFieldBuilder) WithError(err error) TypedFieldBuilder {
	if err == nil {
		return fb
	}
	if fb.state == nil {
		fb = fb.acquire()
	}
	addErr(fb.state, nil, "error", err)
	return fb
}

// Str adds a string field under a pre-declared key (fastest path — the
// pre-escaped `,"key":` fragment is a single memcpy, never escaped at log
// time). Pair with a package-level key from Key(...).
func (fb TypedFieldBuilder) Str(key *types.FieldKey, value string) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addStr(fb.state, key, key.Name, value)
	return fb
}

// Int adds an int field under a pre-declared key.
func (fb TypedFieldBuilder) Int(key *types.FieldKey, value int) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addInt(fb.state, key, key.Name, value)
	return fb
}

// Int64 adds an int64 field under a pre-declared key.
func (fb TypedFieldBuilder) Int64(key *types.FieldKey, value int64) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addInt64(fb.state, key, key.Name, value)
	return fb
}

// Float64 adds a float64 field under a pre-declared key.
func (fb TypedFieldBuilder) Float64(key *types.FieldKey, value float64) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addFloat64(fb.state, key, key.Name, value)
	return fb
}

// Bool adds a bool field under a pre-declared key.
func (fb TypedFieldBuilder) Bool(key *types.FieldKey, value bool) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addBool(fb.state, key, key.Name, value)
	return fb
}

// Err adds an error field under a pre-declared key. A nil error is a no-op.
func (fb TypedFieldBuilder) Err(key *types.FieldKey, err error) TypedFieldBuilder {
	if err == nil {
		return fb
	}
	if fb.state == nil {
		fb = fb.acquire()
	}
	addErr(fb.state, key, key.Name, err)
	return fb
}

// Any adds an arbitrary value under a pre-declared key. Prefer the typed methods
// on the hot path; Any is a convenience for values whose type is not known ahead
// of time.
func (fb TypedFieldBuilder) Any(key *types.FieldKey, value interface{}) TypedFieldBuilder {
	if fb.state == nil {
		fb = fb.acquire()
	}
	addField(fb.state, key, key.Name, types.AnyValue(value))
	return fb
}

// Terminals without fields enter the message-only path through the hot
// function, exactly as Logger.Info does, so a recorded call site is this
// terminal's caller either way.

// Info logs an info message with the accumulated typed fields.
func (fb TypedFieldBuilder) Info(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().infoFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.InfoLevel, msg, terminalCallerFrames)
}

// Debug logs a debug message with the accumulated typed fields.
func (fb TypedFieldBuilder) Debug(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().debugFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.DebugLevel, msg, terminalCallerFrames)
}

// Warn logs a warning message with the accumulated typed fields.
func (fb TypedFieldBuilder) Warn(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().warnFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.WarnLevel, msg, terminalCallerFrames)
}

// Error logs an error message with the accumulated typed fields.
func (fb TypedFieldBuilder) Error(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().errorFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.ErrorLevel, msg, terminalCallerFrames)
}

// Trace logs a trace message with the accumulated typed fields.
func (fb TypedFieldBuilder) Trace(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().traceFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.TraceLevel, msg, terminalCallerFrames)
}

// Fatal logs a fatal message with the accumulated typed fields, flushes the
// adapters, and terminates the process via the logger's exit function.
func (fb TypedFieldBuilder) Fatal(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().fatalFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.FatalLevel, msg, terminalCallerFrames)
}

// Panic logs a panic message with the accumulated typed fields, then panics
// with the message.
func (fb TypedFieldBuilder) Panic(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().panicFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.PanicLevel, msg, terminalCallerFrames)
}

// dispatchLine renders the accumulated line and returns the state to the pool.
// It is the single dispatch point shared by the classic FieldBuilder, the
// typed builder, and the level-first Line API, so level filtering, sampling,
// masking, metrics, terminal-level semantics, and state release are enforced
// identically for every fluent API. frames counts the frames between
// dispatchLine and the application's logging call (terminalCallerFrames or
// messageCallerFrames); it is read only when the logger records call sites.
func dispatchLine(l *Logger, s *perPState, epoch uint32, level types.LogLevel, msg string, frames int) {
	// Stale-builder guard: the state was already dispatched and recycled
	// (documented misuse: two terminals on one builder). Touching it now
	// would corrupt whoever owns it next — make the call a no-op instead.
	if s.epoch != epoch {
		return
	}

	if level < l.Level() {
		globalPerPPool.put(s)
		return
	}

	// Direct fast path: fields are already encoded bytes; assemble header +
	// bound context + fields + closer in the pooled line buffer and hand the
	// finished line to the raw writer. Every call here is statically
	// dispatched on the concrete JSON formatter. Eligibility (see NewLogger)
	// guarantees masking and sampling are off, so no transform is skipped.
	if f := s.directJSON; f != nil {
		line := f.AppendHeader(s.lineBuf[:0], l.clock.GetNsecValue(), level, msg)
		if l.recordCaller {
			line = append(line, callerMember(l.callSiteAt(frames))...) // rendered once per site
		}
		if len(l.boundBytes) > 0 {
			line = append(line, l.boundBytes...) // whole bound context: one memcpy
		}
		line = append(line, s.directFields...)
		line = jsonfmt.AppendCloser(line)
		s.lineBuf = line // retain growth for reuse
		_ = l.rawWriter.WriteRaw(line)
		if l.metrics != nil {
			l.metrics.counts[level].Add(1)
		}
		globalPerPPool.put(s)
	} else {
		entry := &s.entry
		entry.Level = level
		entry.Message = msg
		entry.Component = l.component
		entry.TimestampUnix = l.clock.GetNsecValue()
		entry.StaticFields = entry.StaticFields[:entry.StaticFieldCount]

		// Sampling drops before the (more expensive) masking transform. Fatal
		// and Panic lines are never sampled away — losing the last line before
		// a crash is the one drop an operator cannot afford.
		if l.sampler != nil && level < types.FatalLevel && !l.sampler.ShouldSample(entry) {
			globalPerPPool.put(s)
			return
		}

		// The call site is resolved after sampling, so a dropped line pays no
		// stack walk; formatters render it from File/Line (see AppendCaller).
		if l.recordCaller {
			site := l.callSiteAt(frames)
			entry.File, entry.Line = site.File, site.Line
		}

		if l.enableMasking && l.masker != nil {
			l.masker.Apply(entry)
		}

		switch {
		case l.discardAdapter != nil:
			_ = l.discardAdapter.WriteZero(nil)
		case len(l.adapters) == 1:
			_ = l.adapters[0].WriteZero(entry)
		default:
			for _, a := range l.adapters {
				_ = a.WriteZero(entry)
			}
		}

		if l.recordCaller {
			entry.File, entry.Line = "", 0 // the pooled entry must not carry the site on
		}

		if l.metrics != nil {
			l.metrics.counts[level].Add(1)
		}

		globalPerPPool.put(s)
	}

	// Terminal-level semantics on the SHARED tail, applied on both encode
	// paths after the state is safely back in the pool (a recovered panic
	// leaks nothing). When this switch lived on the capture branch only,
	// Typed().Fatal on a direct-eligible logger wrote the line but skipped
	// the flush-and-exit contract.
	switch level {
	case types.FatalLevel:
		_ = l.Flush()
		l.exit(1)
	case types.PanicLevel:
		panic(msg)
	}
}
