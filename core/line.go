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

// Package core — level-first Line API.
// Author: Admilson B. F. Cossa

package core

import (
	"github.com/go-gen-ecosystem/halolog/types"
)

// Line is a level-first fluent builder: the level is fixed when the line is
// opened, so a DISABLED level costs a single check — no state acquisition, no
// field encoding (the zerolog model). Line is two words; a nil state marks a
// disabled line and every method no-ops on it.
//
//	logger.InfoLine().Str(keyUser, "alice").WithInt("status", 200).Msg("handled")
//
// A Line must be finished with exactly one Msg/Send. Using it afterwards is a
// documented misuse; the pooled state's epoch guard turns such calls into
// no-ops instead of corrupting a line another goroutine now owns.
//
// Field and dispatch mechanics are shared with the typed builder (addField /
// dispatchLine), so both APIs stay byte-identical and zero-allocation.
type Line struct {
	s     *perPState
	epoch uint32 // state generation captured at open
}

// line opens a level-first line, returning the disabled (no-op) Line when the
// level is filtered out.
func (l *Logger) line(level types.LogLevel) Line {
	if level < l.Level() {
		return Line{}
	}
	s := acquireState(l)
	s.owner = l
	s.lineLevel = level
	return Line{s: s, epoch: s.epoch}
}

// TraceLine opens a TRACE-level line.
func (l *Logger) TraceLine() Line { return l.line(types.TraceLevel) }

// DebugLine opens a DEBUG-level line.
func (l *Logger) DebugLine() Line { return l.line(types.DebugLevel) }

// InfoLine opens an INFO-level line.
func (l *Logger) InfoLine() Line { return l.line(types.InfoLevel) }

// WarnLine opens a WARN-level line.
func (l *Logger) WarnLine() Line { return l.line(types.WarnLevel) }

// ErrorLine opens an ERROR-level line.
func (l *Logger) ErrorLine() Line { return l.line(types.ErrorLevel) }

// live reports whether the line may still accept fields: not disabled and not
// already dispatched (epoch guard).
func (ln Line) live() bool {
	return ln.s != nil && ln.s.epoch == ln.epoch
}

// Str adds a string field under a pre-declared key.
func (ln Line) Str(key *types.FieldKey, value string) Line {
	if ln.live() {
		addStr(ln.s, key, keyName(key), value)
	}
	return ln
}

// Int adds an int field under a pre-declared key.
func (ln Line) Int(key *types.FieldKey, value int) Line {
	if ln.live() {
		addInt(ln.s, key, keyName(key), value)
	}
	return ln
}

// Int64 adds an int64 field under a pre-declared key.
func (ln Line) Int64(key *types.FieldKey, value int64) Line {
	if ln.live() {
		addInt64(ln.s, key, keyName(key), value)
	}
	return ln
}

// Float64 adds a float64 field under a pre-declared key.
func (ln Line) Float64(key *types.FieldKey, value float64) Line {
	if ln.live() {
		addFloat64(ln.s, key, keyName(key), value)
	}
	return ln
}

// Bool adds a bool field under a pre-declared key.
func (ln Line) Bool(key *types.FieldKey, value bool) Line {
	if ln.live() {
		addBool(ln.s, key, keyName(key), value)
	}
	return ln
}

// Err adds an error field under a pre-declared key. A nil error is a no-op.
func (ln Line) Err(key *types.FieldKey, err error) Line {
	if err != nil && ln.live() {
		addErr(ln.s, key, keyName(key), err)
	}
	return ln
}

// Any adds an arbitrary value under a pre-declared key. Prefer the typed
// methods on hot paths.
func (ln Line) Any(key *types.FieldKey, value interface{}) Line {
	if ln.live() {
		addField(ln.s, key, keyName(key), types.AnyValue(value))
	}
	return ln
}

// WithString adds a string field under a plain string key.
func (ln Line) WithString(key, value string) Line {
	if ln.live() {
		addStr(ln.s, nil, key, value)
	}
	return ln
}

// WithInt adds an int field under a plain string key.
func (ln Line) WithInt(key string, value int) Line {
	if ln.live() {
		addInt(ln.s, nil, key, value)
	}
	return ln
}

// WithInt64 adds an int64 field under a plain string key.
func (ln Line) WithInt64(key string, value int64) Line {
	if ln.live() {
		addInt64(ln.s, nil, key, value)
	}
	return ln
}

// WithFloat64 adds a float64 field under a plain string key.
func (ln Line) WithFloat64(key string, value float64) Line {
	if ln.live() {
		addFloat64(ln.s, nil, key, value)
	}
	return ln
}

// WithBool adds a bool field under a plain string key.
func (ln Line) WithBool(key string, value bool) Line {
	if ln.live() {
		addBool(ln.s, nil, key, value)
	}
	return ln
}

// WithError adds an error field under the "error" key. A nil error is a no-op.
func (ln Line) WithError(err error) Line {
	if err != nil && ln.live() {
		addErr(ln.s, nil, "error", err)
	}
	return ln
}

// Msg completes the line with a message and writes it. On a disabled line it
// is a no-op. The Line must not be used after Msg; a second Msg on the same
// Line is caught by the epoch guard and does nothing.
func (ln Line) Msg(msg string) {
	if ln.s == nil || ln.s.epoch != ln.epoch {
		return
	}
	dispatchLine(ln.s.owner, ln.s, ln.epoch, ln.s.lineLevel, msg, terminalCallerFrames)
}

// Send completes the line with an empty message. It dispatches directly
// rather than through Msg, so a recorded call site is Send's caller.
func (ln Line) Send() {
	if ln.s == nil || ln.s.epoch != ln.epoch {
		return
	}
	dispatchLine(ln.s.owner, ln.s, ln.epoch, ln.s.lineLevel, "", terminalCallerFrames)
}

// keyName returns the raw name of a pre-declared key (nil-safe).
func keyName(key *types.FieldKey) string {
	if key == nil {
		return ""
	}
	return key.Name
}
