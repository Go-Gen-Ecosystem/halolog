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

// Package types coverage tests for the log entry lifecycle, levels, and
// field aggregation helpers.
// @author Admilson B. F. Cossa

package types

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLogLevel_String_AllLevels(t *testing.T) {
	tests := []struct {
		level LogLevel
		want  string
	}{
		{TraceLevel, "TRACE"},
		{DebugLevel, "DEBUG"},
		{InfoLevel, "INFO"},
		{WarnLevel, "WARN"},
		{ErrorLevel, "ERROR"},
		{FatalLevel, "FATAL"},
		{PanicLevel, "PANIC"},
		{LogLevel(999), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("LogLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestLogLevel_PriorityAndValidity(t *testing.T) {
	if InfoLevel.Priority() != int(InfoLevel) {
		t.Errorf("Priority() = %d, want %d", InfoLevel.Priority(), int(InfoLevel))
	}
	if ErrorLevel.Priority() <= WarnLevel.Priority() {
		t.Error("ErrorLevel priority should exceed WarnLevel priority")
	}

	if !TraceLevel.IsDefined() {
		t.Error("TraceLevel should be defined")
	}
	if !PanicLevel.IsDefined() {
		t.Error("PanicLevel should be defined")
	}
	if LogLevel(100).IsDefined() {
		t.Error("LogLevel(100) should not be defined")
	}

	if !InfoLevel.IsValid() {
		t.Error("InfoLevel should be valid")
	}
	if LogLevel(50).IsValid() {
		t.Error("LogLevel(50) should not be valid")
	}
}

func TestLogEntry_SettersAndFieldMethods(t *testing.T) {
	e := NewLogEntry(InfoLevel, "hello")
	if e.Level != InfoLevel || e.Message != "hello" {
		t.Fatalf("NewLogEntry did not set level/message: %+v", e)
	}

	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	e.SetTimestamp(ts)
	if !e.Timestamp.Equal(ts) {
		t.Errorf("SetTimestamp did not set Timestamp")
	}
	if e.TimestampUnix != ts.UnixNano() {
		t.Errorf("SetTimestamp did not set TimestampUnix, got %d want %d", e.TimestampUnix, ts.UnixNano())
	}

	e.SetLevel(ErrorLevel)
	if e.Level != ErrorLevel {
		t.Errorf("SetLevel did not set the level")
	}

	e.SetComponent("svc")
	if e.Component != "svc" {
		t.Errorf("SetComponent did not set component")
	}

	sentinel := errors.New("boom")
	e.SetError(sentinel)
	if !errors.Is(e.Error, sentinel) {
		t.Errorf("SetError did not set error")
	}

	e.AddField("k1", "v1")
	e.AddField("k2", 42)
	if len(e.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(e.Fields))
	}
	if e.Fields[1].Type != TypedFieldInt64 {
		t.Errorf("AddField did not infer int64 type, got %v", e.Fields[1].Type)
	}

	e.AddFields(map[string]interface{}{"a": true, "b": 1.5})
	if len(e.Fields) != 4 {
		t.Errorf("AddFields should have appended 2 more fields, got %d total", len(e.Fields))
	}

	e.AddContext("ctxk", "ctxv")
	if len(e.Context) != 1 || e.Context[0].Key != "ctxk" {
		t.Errorf("AddContext did not add context field")
	}
}

func TestLogEntry_Reset(t *testing.T) {
	e := NewLogEntry(WarnLevel, "msg")
	e.AddField("k", "v")
	e.AddContext("ck", "cv")
	e.SetError(errors.New("err"))
	e.ErrorMsg = "err"
	e.StaticFieldCount = 3
	e.StaticContextCount = 2

	e.Reset()

	if len(e.Fields) != 0 {
		t.Errorf("Reset should truncate Fields, got len %d", len(e.Fields))
	}
	if len(e.Context) != 0 {
		t.Errorf("Reset should truncate Context, got len %d", len(e.Context))
	}
	if e.Error != nil {
		t.Errorf("Reset should clear Error")
	}
	if e.ErrorMsg != "" {
		t.Errorf("Reset should clear ErrorMsg")
	}
	if e.StaticFieldCount != 0 || e.StaticContextCount != 0 {
		t.Errorf("Reset should zero static counts")
	}
}

func TestLogEntry_ToLogEntry(t *testing.T) {
	e := NewLogEntry(InfoLevel, "x")
	if e.ToLogEntry() != e {
		t.Error("ToLogEntry should return the same pointer")
	}
}

func TestLogEntry_GetAllFields_DynamicAndStatic(t *testing.T) {
	e := NewLogEntry(InfoLevel, "x")
	// Static fields
	e.StaticFields = []TypedFieldData{
		{Key: "s1", Value: "sv1", Type: TypedFieldString},
		{Key: "s2", Value: int64(2), Type: TypedFieldInt64},
	}
	e.StaticFieldCount = 2
	// Dynamic fields
	e.AddField("d1", "dv1")

	all := e.GetAllFields()
	if len(all) != 3 {
		t.Fatalf("GetAllFields expected 3, got %d", len(all))
	}
	// Static come first
	if all[0].Key != "s1" || all[1].Key != "s2" || all[2].Key != "d1" {
		t.Errorf("GetAllFields order wrong: %v %v %v", all[0].Key, all[1].Key, all[2].Key)
	}
}

func TestLogEntry_GetAllContext_DynamicAndStatic(t *testing.T) {
	e := NewLogEntry(InfoLevel, "x")
	e.StaticContext = []TypedFieldData{
		{Key: "sc1", Value: "v", Type: TypedFieldString},
	}
	e.StaticContextCount = 1
	e.AddContext("dc1", "v2")

	all := e.GetAllContext()
	if len(all) != 2 {
		t.Fatalf("GetAllContext expected 2, got %d", len(all))
	}
	if all[0].Key != "sc1" || all[1].Key != "dc1" {
		t.Errorf("GetAllContext order wrong: %v %v", all[0].Key, all[1].Key)
	}
}

func TestLogEntry_EnsureDynamicFields(t *testing.T) {
	e := &LogEntry{}
	e.StaticFields = []TypedFieldData{
		{Key: "a", Value: "1", Type: TypedFieldString},
		{Key: "b", Value: "2", Type: TypedFieldString},
	}
	e.StaticFieldCount = 2
	e.StaticContext = []TypedFieldData{
		{Key: "c", Value: "3", Type: TypedFieldString},
	}
	e.StaticContextCount = 1

	e.EnsureDynamicFields()

	if len(e.Fields) != 2 {
		t.Errorf("EnsureDynamicFields should populate Fields, got %d", len(e.Fields))
	}
	if len(e.Context) != 1 {
		t.Errorf("EnsureDynamicFields should populate Context, got %d", len(e.Context))
	}
}

func TestLogEntry_Clone(t *testing.T) {
	e := NewLogEntry(ErrorLevel, "clone-me")
	e.Component = "comp"
	e.File = "file.go"
	e.Line = 10
	e.SetError(errors.New("orig"))
	e.AddField("f1", "v1")
	e.AddContext("c1", "cv1")

	clone := e.Clone()

	if clone == e {
		t.Fatal("Clone should return a new pointer")
	}
	if clone.Message != e.Message || clone.Level != e.Level || clone.Component != e.Component {
		t.Errorf("Clone did not copy core metadata")
	}
	if clone.File != "file.go" || clone.Line != 10 {
		t.Errorf("Clone did not copy source location")
	}
	if len(clone.Fields) != 1 || clone.Fields[0].Key != "f1" {
		t.Errorf("Clone did not copy fields")
	}
	if len(clone.Context) != 1 || clone.Context[0].Key != "c1" {
		t.Errorf("Clone did not copy context")
	}

	// Mutating the clone must not affect the original.
	clone.AddField("f2", "v2")
	if len(e.Fields) != 1 {
		t.Errorf("mutating clone affected original fields")
	}
}

func TestLogEntry_ToString(t *testing.T) {
	e := NewLogEntry(ErrorLevel, "the-message")
	e.Component = "the-component"
	e.File = "the-file.go"
	e.Line = 77
	e.AddField("user", "alice")
	e.SetError(errors.New("kaboom"))

	s := e.ToString()

	for _, want := range []string{"ERROR", "the-message", "the-component", "the-file.go", "77", "user", "alice", "kaboom"} {
		if !strings.Contains(s, want) {
			t.Errorf("ToString() missing %q in %q", want, s)
		}
	}
}

func TestLogEntry_ToString_Minimal(t *testing.T) {
	e := NewLogEntry(InfoLevel, "just-a-message")
	s := e.ToString()
	if !strings.Contains(s, "INFO") || !strings.Contains(s, "just-a-message") {
		t.Errorf("ToString minimal missing content: %q", s)
	}
	// No component / file / fields / error => no bracketed extras
	if strings.Contains(s, "Component:") {
		t.Errorf("ToString should not include component section when empty: %q", s)
	}
}

func TestLogEntryPool_AcquireRelease(t *testing.T) {
	e := AcquireLogEntry()
	if e == nil {
		t.Fatal("AcquireLogEntry returned nil")
	}
	e.Level = ErrorLevel
	e.Message = "used"
	e.AddField("k", "v")
	ReleaseLogEntry(e)

	// After release, references should be cleared.
	if e.Fields != nil {
		t.Errorf("ReleaseLogEntry should nil Fields")
	}
	if e.Error != nil {
		t.Errorf("ReleaseLogEntry should nil Error")
	}

	// Acquire again yields a usable, reset entry.
	e2 := AcquireLogEntry()
	if e2.Message != "" {
		t.Errorf("AcquireLogEntry should reset Message, got %q", e2.Message)
	}
	if e2.Level != 0 {
		t.Errorf("AcquireLogEntry should reset Level, got %v", e2.Level)
	}
	ReleaseLogEntry(e2)
}

func TestReleaseLogEntry_NilSafe(t *testing.T) {
	// Must not panic on nil.
	ReleaseLogEntry(nil)
}

func TestNewLogEntryWithCaller(t *testing.T) {
	_, file, marker, _ := runtime.Caller(0)
	e := NewLogEntryWithCaller(WarnLevel, "with-caller", 0)
	if e == nil {
		t.Fatal("NewLogEntryWithCaller returned nil")
	}
	if e.Level != WarnLevel || e.Message != "with-caller" {
		t.Errorf("NewLogEntryWithCaller did not set fields: %+v", e)
	}
	if e.File != file || e.Line != marker+1 {
		t.Errorf("NewLogEntryWithCaller recorded %s:%d, want %s:%d", e.File, e.Line, file, marker+1)
	}
	ReleaseLogEntry(e)

	for _, skip := range []int{-1, 65} {
		e := NewLogEntryWithCaller(InfoLevel, "out of range", skip)
		if e.File != "" || e.Line != 0 {
			t.Errorf("skip %d recorded %s:%d, want no location", skip, e.File, e.Line)
		}
		ReleaseLogEntry(e)
	}
}

// TestNewLogEntryWithCallerStartsClean pins that the constructor never hands
// out a previous user's payload: it starts from AcquireLogEntry's reset.
func TestNewLogEntryWithCallerStartsClean(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	seed := AcquireLogEntry()
	seed.ErrorMsg = "previous tenant secret"
	seed.StaticFieldCount = 1
	seed.StaticFields = []TypedFieldData{{Key: "tenant_secret", Value: "previous"}}
	seed.StaticContextCount = 1
	seed.StaticContext = []TypedFieldData{{Key: "tenant_context", Value: "previous"}}
	ReleaseLogEntry(seed)
	fresh := NewLogEntryWithCaller(InfoLevel, "fresh record", 0)
	defer ReleaseLogEntry(fresh)
	if fresh.ErrorMsg != "" || fresh.StaticFieldCount != 0 || fresh.StaticContextCount != 0 {
		t.Fatalf("constructor reused payload: ErrorMsg=%q StaticFieldCount=%d StaticContextCount=%d",
			fresh.ErrorMsg, fresh.StaticFieldCount, fresh.StaticContextCount)
	}
}

func TestUnixTimestampNow(t *testing.T) {
	before := time.Now().Unix()
	got := UnixTimestampNow()
	after := time.Now().Unix()
	if got < before-1 || got > after+1 {
		t.Errorf("UnixTimestampNow()=%d not within [%d,%d]", got, before, after)
	}
}
