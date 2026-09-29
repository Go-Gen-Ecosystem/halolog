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
	"github.com/go-gen-ecosystem/halolog/types"
)

// FieldBuilder provides a fluent API for building log entries with fields.
// This is a VALUE TYPE to avoid heap escape - returned by value, not pointer.
//
// Design:
//   - Get per-P pooled state on first field addition (lazy acquisition)
//   - Write fields directly into the pooled entry's static buffer (zero-copy)
//   - All terminals dispatch through dispatchLine, the single point that
//     enforces level filtering, sampling, masking, metrics, and state release
//     for every fluent API.
//
// A builder must be finished with exactly one terminal call (Info, Error, …).
// Calling a second terminal on a copy is a documented misuse; the pooled
// state's epoch guard turns it into a no-op instead of corrupting the pool.
//
// Usage:
//
//	logger.WithField("key", "value").Info("message")
//	logger.WithField("k1", "v1").WithField("k2", 123).Info("message")
type FieldBuilder struct {
	logger *Logger
	state  *perPState // per-P pooled state (acquired on first field)
	epoch  uint32     // state generation captured at acquisition
}

// WithField adds a field to the entry.
// Returns FieldBuilder by VALUE to avoid heap allocation.
// Gets per-P state on first call, writes directly to StaticBuffer.
func (fb FieldBuilder) WithField(key string, value interface{}) FieldBuilder {
	// Get per-P state on first field (lazy acquisition). Deliberately NOT
	// acquireState: these legacy interface{} fields are struct-captured, so
	// the line must take the capture path even on a direct-eligible logger.
	if fb.state == nil {
		fb.state = globalPerPPool.get()
		fb.epoch = fb.state.epoch
	}

	entry := &fb.state.entry
	n := entry.StaticFieldCount

	// Direct buffer population (zero-copy)
	if n < len(entry.StaticFields) {
		entry.StaticFields[n] = types.TypedFieldData{Key: key, Value: value}
		entry.StaticFieldCount = n + 1
	} else {
		// Overflow to dynamic fields (rare case: >64 fields)
		entry.Fields = append(entry.Fields, types.TypedFieldData{Key: key, Value: value})
	}

	return fb
}

// String adds a string field.
func (fb FieldBuilder) String(key string, value string) FieldBuilder {
	return fb.WithField(key, value)
}

// Int adds an integer field.
func (fb FieldBuilder) Int(key string, value int) FieldBuilder {
	return fb.WithField(key, value)
}

// Int64 adds an int64 field.
func (fb FieldBuilder) Int64(key string, value int64) FieldBuilder {
	return fb.WithField(key, value)
}

// Float64 adds a float64 field.
func (fb FieldBuilder) Float64(key string, value float64) FieldBuilder {
	return fb.WithField(key, value)
}

// Bool adds a boolean field.
func (fb FieldBuilder) Bool(key string, value bool) FieldBuilder {
	return fb.WithField(key, value)
}

// Err adds an error field.
func (fb FieldBuilder) Err(err error) FieldBuilder {
	if err != nil {
		return fb.WithField("error", err.Error())
	}
	return fb
}

// Info logs an info message with the accumulated fields.
func (fb FieldBuilder) Info(msg string) {
	if fb.state == nil {
		// No fields: the message-only path, entered through the hot function
		// exactly as Logger.Info does, so a recorded call site is this
		// terminal's caller either way.
		fb.logger.hot.Load().infoFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.InfoLevel, msg, terminalCallerFrames)
}

// Debug logs a debug message with the accumulated fields.
func (fb FieldBuilder) Debug(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().debugFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.DebugLevel, msg, terminalCallerFrames)
}

// Warn logs a warning message with the accumulated fields.
func (fb FieldBuilder) Warn(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().warnFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.WarnLevel, msg, terminalCallerFrames)
}

// Error logs an error message with the accumulated fields.
func (fb FieldBuilder) Error(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().errorFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.ErrorLevel, msg, terminalCallerFrames)
}

// Trace logs a trace message with the accumulated fields.
func (fb FieldBuilder) Trace(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().traceFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.TraceLevel, msg, terminalCallerFrames)
}

// Fatal logs a fatal message with the accumulated fields, flushes the
// adapters, and terminates the process via the logger's exit function
// (os.Exit(1) unless overridden in Config).
func (fb FieldBuilder) Fatal(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().fatalFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.FatalLevel, msg, terminalCallerFrames)
}

// Panic logs a panic message with the accumulated fields, then panics with
// the message.
func (fb FieldBuilder) Panic(msg string) {
	if fb.state == nil {
		fb.logger.hot.Load().panicFunc(fb.logger, msg)
		return
	}
	dispatchLine(fb.logger, fb.state, fb.epoch, types.PanicLevel, msg, terminalCallerFrames)
}
