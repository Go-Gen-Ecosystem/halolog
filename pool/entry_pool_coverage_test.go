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
// Package pool provides object pooling
// @author Admilson B. F. Cossa

package pool

import (
	"errors"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// TestEntryPool_ReleaseClearsSensitiveFields exercises the full reset path in
// ReleaseEntry: dynamic fields, static context, message, level, component and
// timestamps must all be wiped before the entry is returned to the pool.
func TestEntryPool_ReleaseClearsSensitiveFields(t *testing.T) {
	entry := GlobalPool.AcquireEntry()
	entry.Message = "secret"
	entry.Component = "auth"
	entry.Level = types.ErrorLevel
	entry.Timestamp = time.Now()
	entry.TimestampUnix = entry.Timestamp.UnixNano()
	entry.Fields = append(entry.Fields, types.TypedFieldData{Key: "token", Value: "abc"})
	// StaticContext is pre-sized to 16; write into it so the clearing loop runs.
	if len(entry.StaticContext) > 0 {
		entry.StaticContext[0] = types.TypedFieldData{Key: "session", Value: "xyz"}
	}

	GlobalPool.ReleaseEntry(entry)

	// After release the observable state must be zeroed.
	if entry.Message != "" {
		t.Errorf("Message not cleared: %q", entry.Message)
	}
	if entry.Component != "" {
		t.Errorf("Component not cleared: %q", entry.Component)
	}
	if entry.Level != types.InfoLevel {
		t.Errorf("Level not reset to InfoLevel, got %v", entry.Level)
	}
	if !entry.Timestamp.IsZero() {
		t.Errorf("Timestamp not cleared: %v", entry.Timestamp)
	}
	if entry.TimestampUnix != 0 {
		t.Errorf("TimestampUnix not cleared: %d", entry.TimestampUnix)
	}
	if len(entry.Fields) != 0 {
		t.Errorf("Fields not truncated, len=%d", len(entry.Fields))
	}
	if entry.StaticFieldCount != 0 {
		t.Errorf("StaticFieldCount not reset, got %d", entry.StaticFieldCount)
	}
}

// TestEntryPool_ReleaseClearsLineMetadata pins that a released entry keeps
// nothing a later borrower could emit as its own: message-only log paths fill
// in only the level, message, component, and time of a pooled entry.
func TestEntryPool_ReleaseClearsLineMetadata(t *testing.T) {
	entry := GlobalPool.AcquireEntry()
	entry.File = "private-tenant-path.go"
	entry.Line = 417
	entry.Caller = "private-tenant-caller"
	entry.Error = errors.New("private-tenant-error")
	entry.ErrorMsg = "private-tenant-error"
	entry.Context = []types.TypedFieldData{{Key: "tenant_secret", Value: "private-value"}}
	entry.StaticContextCount = 1
	entry.LocationLen = 9
	entry.BaseName = "private.go"
	entry.UseIndexedStorage = true
	context := entry.Context

	GlobalPool.ReleaseEntry(entry)

	if entry.File != "" || entry.Line != 0 || entry.Caller != nil {
		t.Errorf("source location kept: file=%q line=%d caller=%v", entry.File, entry.Line, entry.Caller)
	}
	if entry.Error != nil || entry.ErrorMsg != "" {
		t.Errorf("error kept: err=%v msg=%q", entry.Error, entry.ErrorMsg)
	}
	if len(entry.Context) != 0 || entry.StaticContextCount != 0 || context[0] != (types.TypedFieldData{}) {
		t.Errorf("context kept: len=%d count=%d slot=%+v", len(entry.Context), entry.StaticContextCount, context[0])
	}
	if entry.LocationLen != 0 || entry.BaseName != "" || entry.UseIndexedStorage {
		t.Errorf("location cache or indexed flag kept: len=%d base=%q indexed=%v", entry.LocationLen, entry.BaseName, entry.UseIndexedStorage)
	}
}

// TestEntryPool_Close flips the shutdown flag and, afterwards, AcquireEntry
// must take the basic fallback path rather than the pooled one.
//
// NOTE: EntryPool.Close drains its sync.Pool with
//
//	for { if p.entryPool.Get() == nil { break } }
//
// which only terminates when Get() returns nil. A sync.Pool returns nil from
// Get() only when it is empty AND its New func is nil. The production
// TestEntryPool_Close exercises Close() on a fully-initialized pool — one whose
// entryPool.New is non-nil (as InitializeGlobalPool installs). This used to hang
// forever (Close drained the sync.Pool waiting for a nil that never came); the
// fix removed the drain loop, so Close must now return promptly.
func TestEntryPool_Close(t *testing.T) {
	// A pool with a real New func, exactly like the global pool.
	p := &EntryPool{}
	p.entryPool.New = func() interface{} {
		e := &types.LogEntry{
			StaticFields:  make([]types.TypedFieldData, 16),
			StaticContext: make([]types.TypedFieldData, 16),
		}
		return e
	}
	p.initialized.Store(true)

	stats := p.GetStats()
	if !stats.Initialized {
		t.Fatal("dedicated pool should be initialized")
	}
	if stats.Shutdown {
		t.Fatal("dedicated pool should not be shut down yet")
	}

	// Must return promptly (a hang here is caught by the test timeout).
	p.Close()

	after := p.GetStats()
	if !after.Shutdown {
		t.Error("Close did not set the shutdown flag")
	}

	// After shutdown, AcquireEntry must take the basic fallback and still return
	// a usable, correctly-sized entry.
	fallback := p.AcquireEntry()
	if fallback == nil {
		t.Fatal("AcquireEntry returned nil after Close")
	}
	if cap(fallback.StaticFields) < 16 || cap(fallback.StaticContext) < 16 {
		t.Errorf("fallback entry buffers too small: static=%d context=%d",
			cap(fallback.StaticFields), cap(fallback.StaticContext))
	}
}

// TestEntryPool_CloseNilSafe verifies Close on a nil receiver is a no-op.
func TestEntryPool_CloseNilSafe(t *testing.T) {
	var p *EntryPool
	p.Close() // must not panic
}

// TestEntryPool_AcquireNilReceiverFallback verifies the nil/shutdown guard in
// the pointer-receiver AcquireEntry routes to the basic allocator.
func TestEntryPool_AcquireNilReceiverFallback(t *testing.T) {
	var p *EntryPool
	entry := p.AcquireEntry()
	if entry == nil {
		t.Fatal("nil-receiver AcquireEntry should fall back to a basic entry")
	}
	if cap(entry.StaticFields) < 16 || cap(entry.StaticContext) < 16 {
		t.Error("basic fallback entry has undersized static buffers")
	}
}

// TestEntryPool_GetStatsUninitialized verifies GetStats on an uninitialized
// pool returns a zero-value snapshot rather than panicking.
func TestEntryPool_GetStatsUninitialized(t *testing.T) {
	p := &EntryPool{} // never initialized
	stats := p.GetStats()
	if stats.Initialized || stats.Shutdown {
		t.Errorf("uninitialized pool stats should be zero, got %+v", stats)
	}
	if stats.AcquireCount != 0 || stats.ReleaseCount != 0 {
		t.Errorf("uninitialized pool should report zero counters, got %+v", stats)
	}

	var nilPool *EntryPool
	if nilStats := nilPool.GetStats(); nilStats.Initialized {
		t.Error("nil-receiver GetStats should return zero value")
	}
}

// TestEntryPool_ReleaseUninitializedNoop verifies ReleaseEntry is a no-op when
// the pool is not initialized.
func TestEntryPool_ReleaseUninitializedNoop(t *testing.T) {
	p := &EntryPool{}
	entry := acquireEntryBasic()
	entry.Message = "x"
	// Must not panic and must not touch counters (pool is uninitialized).
	p.ReleaseEntry(entry)
	if got := p.GetStats().ReleaseCount; got != 0 {
		t.Errorf("uninitialized ReleaseEntry incremented counter to %d", got)
	}
}

// TestInitializeGlobalPool_Idempotent verifies the guard clause: a second call
// against an already-initialized global pool leaves the instance untouched.
func TestInitializeGlobalPool_Idempotent(t *testing.T) {
	if GlobalPool == nil {
		t.Fatal("GlobalPool should be initialized by package init()")
	}
	before := GlobalPool
	InitializeGlobalPool() // guard clause should short-circuit
	if GlobalPool != before {
		t.Error("InitializeGlobalPool replaced an already-initialized GlobalPool")
	}
	if !GlobalPool.initialized.Load() {
		t.Error("GlobalPool lost its initialized flag")
	}
}

// TestBasicFallbacks directly drives the package-level basic fallback helpers.
func TestBasicFallbacks(t *testing.T) {
	entry := acquireEntryBasic()
	if entry == nil {
		t.Fatal("acquireEntryBasic returned nil")
	}
	if cap(entry.Fields) < 32 {
		t.Errorf("Fields capacity = %d, want >= 32", cap(entry.Fields))
	}
	if len(entry.StaticFields) != 16 || len(entry.StaticContext) != 16 {
		t.Errorf("static buffers = %d/%d, want 16/16",
			len(entry.StaticFields), len(entry.StaticContext))
	}

	entry.Fields = append(entry.Fields, types.TypedFieldData{Key: "k", Value: "v"})
	entry.StaticFieldCount = 3
	releaseEntryBasic(entry)
	if len(entry.Fields) != 0 {
		t.Errorf("releaseEntryBasic did not truncate Fields, len=%d", len(entry.Fields))
	}
	if entry.StaticFieldCount != 0 {
		t.Errorf("releaseEntryBasic did not reset StaticFieldCount, got %d", entry.StaticFieldCount)
	}

	// nil is tolerated.
	releaseEntryBasic(nil)
}

// TestGlobalReleaseEntry_FallbackWhenShutdown exercises the package-level
// AcquireEntry / ReleaseEntry fallback branches by temporarily pointing
// GlobalPool at a shut-down pool, then restoring the original so the rest of
// the suite (and the core zero-alloc guard) is unaffected.
//
// The stand-in pool is built with a nil entryPool.New so that its Close() drain
// loop terminates (see the BUG note on TestEntryPool_Close); its shutdown flag
// is set directly here rather than via Close to keep the intent explicit.
func TestGlobalReleaseEntry_FallbackWhenShutdown(t *testing.T) {
	original := GlobalPool
	t.Cleanup(func() { GlobalPool = original })

	shut := &EntryPool{}
	shut.initialized.Store(true)
	shut.shutdown.Store(true) // simulate a shut-down pool without draining

	GlobalPool = shut

	// Package-level AcquireEntry / ReleaseEntry must take the basic fallback
	// path (GlobalPool is shut down) without panicking.
	entry := AcquireEntry()
	if entry == nil {
		t.Fatal("package AcquireEntry returned nil under shutdown pool")
	}
	entry.Message = "fallback"
	entry.Fields = append(entry.Fields, types.TypedFieldData{Key: "k", Value: "v"})
	ReleaseEntry(entry) // basic reset path
	if len(entry.Fields) != 0 {
		t.Errorf("fallback ReleaseEntry did not truncate Fields, len=%d", len(entry.Fields))
	}
}
