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

// Package core — bound-context child loggers.
//
// A child logger carries fields bound once at construction and emitted on
// every line. The cost model is the point: the bound fields are encoded to
// their final `,"key":value` bytes exactly once, in Logger(), so on the
// direct path every subsequent line pays a single memcpy for its whole
// context — never per-line re-encoding. On the capture path (masking,
// sampling, multiple adapters) the same bound fields are prepended as
// structured data at state acquisition, so maskers see them and output stays
// byte-identical to the direct path.
// Author: Admilson B. F. Cossa

package core

import (
	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/types"
)

// maxBoundFields caps how many fields a child logger may bind. The capture
// path prepends bound fields into the 64-slot static buffer; capping at half
// leaves ample room for per-line fields and keeps the two paths identical.
const maxBoundFields = 32

// Context accumulates fields for a child logger. It is a value builder in the
// style of zerolog's Context: chain typed setters, then call Logger().
// Building a Context allocates (it happens once per child, off the hot path);
// the loggers it produces log at the usual 0 allocs/op.
//
//	reqLog := logger.With().Str(keyTenant, "acme").WithString("region", "eu").Logger()
//	reqLog.Info("handled")                  // context is one memcpy on this line
type Context struct {
	l      *Logger
	fields []types.TypedFieldData
	bytes  []byte
}

// With opens a context builder for deriving a child logger. The builder
// starts from this logger's existing bound context, so children can be
// derived from children; each level re-binds its full context once.
func (l *Logger) With() Context {
	c := Context{l: l}
	if n := len(l.boundFields); n > 0 {
		c.fields = append(make([]types.TypedFieldData, 0, n), l.boundFields...)
		c.bytes = append(make([]byte, 0, len(l.boundBytes)), l.boundBytes...)
	}
	return c
}

// add binds one field: captured as structured data for the capture path and
// encoded to bytes once for the direct path. Beyond maxBoundFields it is a
// no-op (documented cap).
//
// COPY-ON-APPEND CONTRACT: a Context is a value that callers may branch —
// two children derived from the same intermediate context must never share
// writable backing. Both appends therefore clamp capacity to length
// (three-index slice) first, forcing append to reallocate into a fresh
// array; without this, spare capacity at a branch point let the second
// branch overwrite the first branch's field — and, for the byte form, tear
// its pre-encoded JSON. Cost is one copy per bound field at construction
// time (≤ maxBoundFields, off the hot path); the per-line cost model is
// untouched.
func (c Context) add(kd *types.FieldKey, key string, val types.FieldValue) Context {
	if len(c.fields) >= maxBoundFields {
		return c
	}
	c.fields = append(c.fields[:len(c.fields):len(c.fields)],
		types.TypedFieldData{Key: key, KeyDesc: kd, Val: val})
	c.bytes = jsonfmt.AppendField(c.bytes[:len(c.bytes):len(c.bytes)], kd, key, val)
	return c
}

// Str binds a string field under a pre-declared key.
func (c Context) Str(key *types.FieldKey, value string) Context {
	return c.add(key, keyName(key), types.StringValue(value))
}

// Int binds an int field under a pre-declared key.
func (c Context) Int(key *types.FieldKey, value int) Context {
	return c.add(key, keyName(key), types.IntValue(value))
}

// Int64 binds an int64 field under a pre-declared key.
func (c Context) Int64(key *types.FieldKey, value int64) Context {
	return c.add(key, keyName(key), types.Int64Value(value))
}

// Float64 binds a float64 field under a pre-declared key.
func (c Context) Float64(key *types.FieldKey, value float64) Context {
	return c.add(key, keyName(key), types.Float64Value(value))
}

// Bool binds a bool field under a pre-declared key.
func (c Context) Bool(key *types.FieldKey, value bool) Context {
	return c.add(key, keyName(key), types.BoolValue(value))
}

// Err binds an error field under a pre-declared key. A nil error is a no-op.
func (c Context) Err(key *types.FieldKey, err error) Context {
	if err == nil {
		return c
	}
	return c.add(key, keyName(key), types.ErrorValue(err))
}

// Any binds an arbitrary value under a pre-declared key.
func (c Context) Any(key *types.FieldKey, value interface{}) Context {
	return c.add(key, keyName(key), types.AnyValue(value))
}

// WithString binds a string field under a plain string key.
func (c Context) WithString(key, value string) Context {
	return c.add(nil, key, types.StringValue(value))
}

// WithInt binds an int field under a plain string key.
func (c Context) WithInt(key string, value int) Context {
	return c.add(nil, key, types.IntValue(value))
}

// WithInt64 binds an int64 field under a plain string key.
func (c Context) WithInt64(key string, value int64) Context {
	return c.add(nil, key, types.Int64Value(value))
}

// WithFloat64 binds a float64 field under a plain string key.
func (c Context) WithFloat64(key string, value float64) Context {
	return c.add(nil, key, types.Float64Value(value))
}

// WithBool binds a bool field under a plain string key.
func (c Context) WithBool(key string, value bool) Context {
	return c.add(nil, key, types.BoolValue(value))
}

// WithError binds an error field under the "error" key. A nil error is a no-op.
func (c Context) WithError(err error) Context {
	if err == nil {
		return c
	}
	return c.add(nil, "error", types.ErrorValue(err))
}

// WithAny binds an arbitrary value under a plain string key.
func (c Context) WithAny(key string, value interface{}) Context {
	return c.add(nil, key, types.AnyValue(value))
}

// Logger derives the child logger. The child shares the parent's adapters,
// clock, masker, sampler, metrics, and exit hook; its level is the parent's
// level at derivation time (independent afterwards). Bound state is immutable
// from here on — the returned logger is safe for concurrent use exactly like
// its parent.
func (c Context) Logger() *Logger {
	parent := c.l
	child := &Logger{
		component:      parent.component,
		clock:          parent.clock,
		adapters:       parent.adapters,
		masker:         parent.masker,
		enableMasking:  parent.enableMasking,
		discardAdapter: parent.discardAdapter,
		sampler:        parent.sampler,
		metrics:        parent.metrics,
		exitFunc:       parent.exitFunc,
		recordCaller:   parent.recordCaller,
		callerSkip:     parent.callerSkip,
		rawWriter:      parent.rawWriter,
		directAdapter:  parent.directAdapter,
		boundFields:    c.fields,
		boundBytes:     c.bytes,
	}

	level := parent.Level()
	hot := &hotState{
		level:       uint64(level),
		cachedNanos: uint64(child.clock.GetNsecValue()),
	}
	child.setupFunctionPointers(hot, level)
	child.hot.Store(hot)
	return child
}
