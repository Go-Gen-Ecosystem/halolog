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

// Package core — call-site capture (Builder.Caller).
//
// A logger built with Caller records where each line was logged, as the
// member "caller":"file.go:42". The cost model: every line walks the stack
// once to read its caller's program counter, then looks it up in the
// process-wide call-site cache (internal/callsite). The first line from a
// site resolves it and renders the JSON member once; every later line reuses
// both, so a retained site's lines allocate nothing. The cache retains up to
// 4096 sites; one past that is resolved again on each line (allocating).
// Author: Admilson B. F. Cossa

package core

import (
	"runtime"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/internal/callsite"
	"github.com/go-gen-ecosystem/halolog/types"
)

const (
	// maxCallerSkip bounds CallerSkip: the same climb limit the call-site
	// cache enforces for everyone who asks it for a caller.
	maxCallerSkip = callsite.MaxSkip

	// callSiteFrames is what runtime.Callers skips to reach callSiteAt's
	// caller: runtime.Callers itself and callSiteAt.
	callSiteFrames = 2

	// terminalCallerFrames counts the frames from dispatchLine up to the
	// user's call for fluent terminals (Line.Msg and Send, FieldBuilder and
	// TypedFieldBuilder terminals), and from realFatal or realPanic for
	// Logger.Fatal and Logger.Panic: the entry point, then its caller.
	terminalCallerFrames = 2

	// messageCallerFrames counts the frames from dispatchLine up to the user's
	// call for message-only lines (logger.Info, or a builder terminal without
	// fields): logCaller, the level function, the entry point, its caller.
	messageCallerFrames = 4
)

// callSiteAt returns the call site frames above its caller (dispatchLine, or
// realFatal and realPanic), moved up by the logger's CallerSkip. A frame the
// runtime cannot place yields the zero site, which renders nothing on either
// path.
func (l *Logger) callSiteAt(frames int) *callsite.Site {
	var pcs [1]uintptr
	runtime.Callers(callSiteFrames+frames+l.callerSkip, pcs[:])
	return callsite.Lookup(pcs[0])
}

// callerMember is the site's `,"caller":"file.go:42"` member for the direct
// path, rendered once per site by the JSON formatter's own AppendCaller.
func callerMember(site *callsite.Site) []byte {
	return site.Member(jsonfmt.AppendCaller)
}

// clampCallerSkip keeps a configured skip within [0, maxCallerSkip].
func clampCallerSkip(skip int) int {
	return min(max(skip, 0), maxCallerSkip)
}

// ===== CALL-SITE MESSAGE FUNCTIONS (loggers built with Caller) =====

// logCaller routes a message-only line through the pooled path, so that
// dispatchLine, the single dispatch point, records its call site while still
// applying bound context, sampling, masking, metrics, and terminal semantics.
func (l *Logger) logCaller(level types.LogLevel, msg string) {
	s := acquireState(l)
	dispatchLine(l, s, s.epoch, level, msg, messageCallerFrames)
}

// The level functions are plain functions rather than method values: a bound
// method value adds a wrapper frame to every stack walk, and those extra
// frames overflow the runtime's small pcvalue cache, roughly doubling the
// cost of each message-only line.

func traceCaller(l *Logger, msg string) { l.logCaller(types.TraceLevel, msg) }
func debugCaller(l *Logger, msg string) { l.logCaller(types.DebugLevel, msg) }
func infoCaller(l *Logger, msg string)  { l.logCaller(types.InfoLevel, msg) }
func warnCaller(l *Logger, msg string)  { l.logCaller(types.WarnLevel, msg) }
func errorCaller(l *Logger, msg string) { l.logCaller(types.ErrorLevel, msg) }
func fatalCaller(l *Logger, msg string) { l.logCaller(types.FatalLevel, msg) }
func panicCaller(l *Logger, msg string) { l.logCaller(types.PanicLevel, msg) }
