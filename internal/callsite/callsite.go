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

// Package callsite resolves logging call sites once and caches them by
// return program counter. core's Builder.Caller and
// types.NewLogEntryWithCaller both use it, so they report a location the
// same way and share one cache.
//
// The first sighting of a PC resolves it through the runtime (CallersFrames
// owns the return-PC adjustment and inlined frames); that is the only
// allocating step. Keys are code addresses, bounded by the binary rather than
// by input: the grow-only, read-mostly shape sync.Map is built for.
// maxSites caps retention regardless.
// Author: Admilson B. F. Cossa
package callsite

import (
	"runtime"
	"sync"
	"sync/atomic"
)

const (
	// maxSites caps the sites retained process-wide. Past it a site is
	// resolved again on each lookup (allocating) instead of retained.
	maxSites = 4096

	// MaxSkip bounds how far callers may ask Caller to climb: a deeper chain
	// is a configuration error, and the bound keeps frame arithmetic far from
	// integer overflow.
	MaxSkip = 64

	// callerFrames is what runtime.Callers skips inside Caller to reach the
	// caller of Caller's caller: runtime.Callers, Caller, and that caller.
	callerFrames = 3
)

// Site is one resolved call site, immutable once published and shared by
// every lookup of its PC. The zero Site stands for a frame the runtime
// cannot place.
type Site struct {
	File string // runtime path, as runtime.Frame reports it
	Line int

	member atomic.Pointer[[]byte] // rendered once by Member
}

var (
	sites    sync.Map // return PC -> *Site
	retained atomic.Int64
	unknown  = &Site{}
)

// Lookup returns the site of a return PC, resolving it on first sight. A
// zero PC, or one the runtime cannot place, yields the zero Site.
func Lookup(pc uintptr) *Site {
	if pc == 0 {
		return unknown
	}
	if v, ok := sites.Load(pc); ok {
		return v.(*Site)
	}
	return resolve(pc)
}

// resolve renders a site on its first lookup and retains it, up to
// maxSites. It lives apart from Lookup so that handing a PC slice to
// CallersFrames cannot move a caller's buffer to the heap on every lookup.
func resolve(pc uintptr) *Site {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	site := unknown
	if frame.File != "" && frame.Line > 0 {
		site = &Site{File: frame.File, Line: frame.Line}
	}
	if retained.Add(1) > maxSites {
		retained.Add(-1)
		return site
	}
	if v, loaded := sites.LoadOrStore(pc, site); loaded {
		retained.Add(-1)
		return v.(*Site)
	}
	return site
}

// Caller returns the site skip frames above the function calling Caller:
// skip 0 is that function's caller. A skip outside [0, MaxSkip] yields the
// zero Site.
func Caller(skip int) *Site {
	var pcs [1]uintptr
	if skip < 0 || skip > MaxSkip {
		return unknown
	}
	runtime.Callers(callerFrames+skip, pcs[:])
	return Lookup(pcs[0])
}

// Retained reports how many sites are cached.
func Retained() int { return int(retained.Load()) }

// Member returns the site's pre-rendered representation, calling render on
// first use and reusing the result afterwards. It returns nil for the zero
// Site. A site keeps one rendering, so every caller must pass the same
// deterministic render (core passes the JSON formatter's AppendCaller);
// concurrent first uses may both call it, and the first result published wins.
func (s *Site) Member(render func(dst []byte, file string, line int) []byte) []byte {
	if s.File == "" {
		return nil
	}
	if m := s.member.Load(); m != nil {
		return *m
	}
	m := render(nil, s.File, s.Line)
	s.member.CompareAndSwap(nil, &m)
	return *s.member.Load()
}
