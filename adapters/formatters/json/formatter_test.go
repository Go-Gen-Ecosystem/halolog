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
// Package adapters provides output adapters
// Author: Admilson B. F. Cossa

package json

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// --- Constructor Tests ---

func TestNewJSONFormatter(t *testing.T) {
	formatter := NewJsonFormatter()
	if formatter == nil {
		t.Fatal("NewJsonFormatter returned nil")
	}
}

// --- Nil Entry Tests ---

func TestFormat_NilEntry(t *testing.T) {
	formatter := NewJsonFormatter()
	dst := make([]byte, 0, 1024)

	result := formatter.Format(nil, dst)
	if len(result) != 0 {
		t.Errorf("Expected empty result for nil entry, got %d bytes", len(result))
	}
}

// --- Simple Message Tests ---

func TestFormat_SimpleMessage(t *testing.T) {
	formatter := NewJsonFormatter()
	now := time.Date(2025, 11, 24, 12, 0, 0, 0, time.UTC)

	entry := &types.LogEntry{
		Timestamp: now,
		Level:     types.InfoLevel,
		Message:   "test message",
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	// Verify valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v\nOutput: %s", err, string(result))
	}

	// Verify fields
	if parsed["level"] != "INFO" {
		t.Errorf("Expected level INFO, got %v", parsed["level"])
	}

	if parsed["message"] != "test message" {
		t.Errorf("Expected message 'test message', got %v", parsed["message"])
	}

	if _, ok := parsed["time"]; !ok {
		t.Error("Missing time field")
	}
}

// --- All Log Levels Tests ---

func TestFormat_AllLevels(t *testing.T) {
	formatter := NewJsonFormatter()

	testCases := []struct {
		level    types.LogLevel
		expected string
	}{
		{types.TraceLevel, "TRACE"},
		{types.DebugLevel, "DEBUG"},
		{types.InfoLevel, "INFO"},
		{types.WarnLevel, "WARN"},
		{types.ErrorLevel, "ERROR"},
		{types.FatalLevel, "FATAL"},
		{types.PanicLevel, "PANIC"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			entry := &types.LogEntry{
				Timestamp: time.Now(),
				Level:     tc.level,
				Message:   "test",
			}

			dst := make([]byte, 0, 1024)
			result := formatter.Format(entry, dst)

			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("Invalid JSON: %v", err)
			}

			if parsed["level"] != tc.expected {
				t.Errorf("Expected level %s, got %v", tc.expected, parsed["level"])
			}
		})
	}
}

// --- Invalid Level Tests ---

func TestFormat_InvalidLevel(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.LogLevel(99), // Invalid level
		Message:   "test",
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// Should default to PANIC (level 6)
	if parsed["level"] != "PANIC" {
		t.Errorf("Expected level PANIC for invalid level, got %v", parsed["level"])
	}
}

// --- Caller Info Tests ---

func TestFormat_WithCallerInfo(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
		File:      "/path/to/file.go",
		Line:      42,
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	caller, ok := parsed["caller"].(string)
	if !ok {
		t.Fatal("Missing caller field")
	}

	if !strings.Contains(caller, "file.go:42") {
		t.Errorf("Expected caller to contain 'file.go:42', got %s", caller)
	}
}

// --- Windows Path Tests ---

func TestFormat_WindowsPath(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
		File:      "C:\\Users\\Admin\\project\\main.go",
		Line:      123,
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	caller, ok := parsed["caller"].(string)
	if !ok {
		t.Fatal("Missing caller field")
	}

	if !strings.Contains(caller, "main.go:123") {
		t.Errorf("Expected basename 'main.go:123', got %s", caller)
	}

	if strings.Contains(caller, "C:\\") {
		t.Errorf("Should not contain full Windows path, got %s", caller)
	}
}

// --- No Caller Info Tests ---

func TestFormat_NoCallerInfo(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
		File:      "",
		Line:      0,
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if _, ok := parsed["caller"]; ok {
		t.Error("Should not have caller field when File is empty")
	}
}

// --- JSON Escaping Tests ---

func TestFormat_JSONEscaping(t *testing.T) {
	formatter := NewJsonFormatter()

	testCases := []struct {
		name     string
		message  string
		expected string
	}{
		{
			name:     "quotes",
			message:  `Hello "World"`,
			expected: `Hello \"World\"`,
		},
		{
			name:     "backslash",
			message:  `C:\path\to\file`,
			expected: `C:\\path\\to\\file`,
		},
		{
			name:     "newline",
			message:  "Line1\nLine2",
			expected: `Line1\nLine2`,
		},
		{
			name:     "tab",
			message:  "Col1\tCol2",
			expected: `Col1\tCol2`,
		},
		{
			name:     "carriage_return",
			message:  "Before\rAfter",
			expected: `Before\rAfter`,
		},
		{
			name:     "control_chars",
			message:  "Test\x00\x01\x02",
			expected: `Test\u0000\u0001\u0002`,
		},
		{
			name:     "mixed",
			message:  `"Hello"\nWorld\t!`,
			expected: `\"Hello\"\\nWorld\\t!`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &types.LogEntry{
				Timestamp: time.Now(),
				Level:     types.InfoLevel,
				Message:   tc.message,
			}

			dst := make([]byte, 0, 1024)
			result := formatter.Format(entry, dst)

			// Verify valid JSON
			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("Invalid JSON: %v\nOutput: %s", err, string(result))
			}

			// Verify escaped content
			if !strings.Contains(string(result), tc.expected) {
				t.Errorf("Expected escaped string to contain %q, got %s", tc.expected, string(result))
			}
		})
	}
}

// --- No Escaping Needed Tests ---

func TestFormat_NoEscapingNeeded(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "Simple ASCII message without special chars",
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if parsed["message"] != entry.Message {
		t.Errorf("Message mismatch: expected %s, got %v", entry.Message, parsed["message"])
	}
}

// --- Fields Tests ---

func TestFormat_WithFields(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
		Fields: []types.TypedFieldData{
			{Key: "user_id", Value: "user123", Type: types.TypedFieldString},
			{Key: "request_id", Value: "req456", Type: types.TypedFieldString},
		},
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v\nOutput: %s", err, string(result))
	}

	if parsed["user_id"] != "user123" {
		t.Errorf("Expected user_id 'user123', got %v", parsed["user_id"])
	}

	if parsed["request_id"] != "req456" {
		t.Errorf("Expected request_id 'req456', got %v", parsed["request_id"])
	}
}

// --- Fields with Escaping Tests ---

func TestFormat_FieldsWithEscaping(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
		Fields: []types.TypedFieldData{
			{Key: "data", Value: `"quoted"\nvalue`, Type: types.TypedFieldString},
		},
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v\nOutput: %s", err, string(result))
	}

	// Should properly decode escaped characters
	if !strings.Contains(parsed["data"].(string), `"quoted"`) {
		t.Errorf("Field not properly unescaped: %v", parsed["data"])
	}
}

// --- Empty Fields Tests ---

func TestFormat_EmptyFields(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
		Fields:    []types.TypedFieldData{},
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// Should not have any extra fields beyond time, level, message
	if len(parsed) > 3 {
		t.Errorf("Expected only 3 fields, got %d: %v", len(parsed), parsed)
	}
}

// --- Timestamp Cache Tests ---

func TestFormat_TimestampCache(t *testing.T) {
	formatter := NewJsonFormatter()
	now := time.Now()

	entry := &types.LogEntry{
		Timestamp: now,
		Level:     types.InfoLevel,
		Message:   "test",
	}

	dst := make([]byte, 0, 1024)

	// First call: cache miss
	result1 := formatter.Format(entry, dst)

	// Second call: cache hit (same second)
	result2 := formatter.Format(entry, dst[:0])

	// Both should produce identical output
	if string(result1) != string(result2) {
		t.Error("Cache hit should produce identical output to cache miss")
	}

	// Verify valid JSON on both
	var parsed map[string]interface{}
	if err := json.Unmarshal(result1, &parsed); err != nil {
		t.Fatalf("Invalid JSON on first call: %v", err)
	}
	if err := json.Unmarshal(result2, &parsed); err != nil {
		t.Fatalf("Invalid JSON on second call: %v", err)
	}
}

// --- Timestamp Cache Update Tests ---

func TestFormat_TimestampCacheUpdate(t *testing.T) {
	formatter := NewJsonFormatter()

	time1 := time.Unix(1000, 0)
	time2 := time.Unix(2000, 0)

	entry1 := &types.LogEntry{
		Timestamp: time1,
		Level:     types.InfoLevel,
		Message:   "first",
	}

	entry2 := &types.LogEntry{
		Timestamp: time2,
		Level:     types.InfoLevel,
		Message:   "second",
	}

	dst := make([]byte, 0, 1024)

	result1 := formatter.Format(entry1, dst)

	// Use a fresh buffer for the second result to test proper buffer management
	dst2 := make([]byte, 0, 1024)
	result2 := formatter.Format(entry2, dst2)

	// Should have different timestamps
	if string(result1) == string(result2) {
		t.Error("Different timestamps should produce different output")
	}

	// Both should be valid JSON
	var parsed1, parsed2 map[string]interface{}
	if err := json.Unmarshal(result1, &parsed1); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}
	if err := json.Unmarshal(result2, &parsed2); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if parsed1["time"] == parsed2["time"] {
		t.Error("Time fields should be different")
	}
}

// --- Small Buffer Tests ---

func TestFormat_SmallBuffer(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test message that is somewhat long",
	}

	// Very small buffer
	dst := make([]byte, 0, 10)
	result := formatter.Format(entry, dst)

	// Should still produce valid output (may truncate)
	if len(result) == 0 {
		t.Error("Expected non-empty result even with small buffer")
	}
}

// --- Zero Capacity Buffer Tests ---

func TestFormat_ZeroCapacityBuffer(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
	}

	var dst []byte
	result := formatter.Format(entry, dst)

	// Should still produce output
	if len(result) == 0 {
		t.Error("Expected non-empty result even with zero capacity")
	}
}

// --- Large Message Tests ---

func TestFormat_LargeMessage(t *testing.T) {
	formatter := NewJsonFormatter()

	// Message larger than typical
	largeMsg := strings.Repeat("x", 2000)

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   largeMsg,
	}

	dst := make([]byte, 0, 4096)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// Message should be present (possibly truncated)
	if msg, ok := parsed["message"].(string); !ok || len(msg) == 0 {
		t.Error("Expected non-empty message")
	}
}

// --- Empty Message Tests ---

func TestFormat_EmptyMessage(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "",
	}

	dst := make([]byte, 0, 1024)
	result := formatter.Format(entry, dst)

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if msg, ok := parsed["message"].(string); !ok || msg != "" {
		t.Errorf("Expected empty message, got %v", parsed["message"])
	}
}

// --- Concurrent Usage Tests ---

func TestFormat_ConcurrentUsage(t *testing.T) {
	formatter := NewJsonFormatter()
	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	errors := make(chan error, goroutines*iterations)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()

			entry := &types.LogEntry{
				Timestamp: time.Now(),
				Level:     types.InfoLevel,
				Message:   "concurrent test",
			}

			for j := 0; j < iterations; j++ {
				dst := make([]byte, 0, 1024)
				result := formatter.Format(entry, dst)

				// Verify valid JSON
				var parsed map[string]interface{}
				if err := json.Unmarshal(result, &parsed); err != nil {
					errors <- err
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent test error: %v", err)
	}
}

// --- Concurrent Cache Tests ---

func TestFormat_ConcurrentCacheAccess(t *testing.T) {
	formatter := NewJsonFormatter()
	const goroutines = 50
	const iterations = 500

	// Use same timestamp to test cache contention
	now := time.Now()

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			entry := &types.LogEntry{
				Timestamp: now,
				Level:     types.InfoLevel,
				Message:   "test",
			}

			for j := 0; j < iterations; j++ {
				dst := make([]byte, 0, 1024)
				result := formatter.Format(entry, dst)

				// Verify valid JSON (shouldn't have race issues)
				var parsed map[string]interface{}
				if err := json.Unmarshal(result, &parsed); err != nil {
					t.Errorf("Race condition in cache: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}

// --- Small Int Cache Tests ---

func TestFormat_SmallIntCache(t *testing.T) {
	formatter := NewJsonFormatter()

	testCases := []int{0, 1, 5, 9, 10, 42, 99, 100, 999, 1000}

	for _, line := range testCases {
		t.Run(fmt.Sprintf("line_%d", line), func(t *testing.T) {
			entry := &types.LogEntry{
				Timestamp: time.Now(),
				Level:     types.InfoLevel,
				Message:   "test",
				File:      "test.go",
				Line:      line,
			}

			dst := make([]byte, 0, 1024)
			result := formatter.Format(entry, dst)

			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("Invalid JSON: %v", err)
			}

			caller, ok := parsed["caller"].(string)
			if !ok {
				t.Fatal("Missing caller field")
			}

			expected := fmt.Sprintf("test.go:%d", line)
			if !strings.Contains(caller, expected) {
				t.Errorf("Expected caller to contain %q, got %s", expected, caller)
			}
		})
	}
}

// --- Buffer Reuse Tests ---

func TestFormat_BufferReuse(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "test",
	}

	dst := make([]byte, 0, 1024)

	// Format multiple times with same buffer
	for i := 0; i < 10; i++ {
		dst = formatter.Format(entry, dst[:0])

		var parsed map[string]interface{}
		if err := json.Unmarshal(dst, &parsed); err != nil {
			t.Fatalf("Iteration %d: Invalid JSON: %v", i, err)
		}
	}
}

// --- Zero Allocation Tests ---

func TestFormat_ZeroAllocation(t *testing.T) {
	formatter := NewJsonFormatter()

	entry := &types.LogEntry{
		Timestamp: time.Now(),
		Level:     types.InfoLevel,
		Message:   "zero alloc test",
		File:      "test.go",
		Line:      42,
	}

	dst := make([]byte, 0, 1024)

	// Warm up
	dst = formatter.Format(entry, dst[:0])

	// Measure allocations
	allocs := testing.AllocsPerRun(1000, func() {
		dst = formatter.Format(entry, dst[:0])
	})

	if allocs > 0 {
		t.Fatalf("CRITICAL: Zero-allocation guarantee violated - got %.2f allocs/op", allocs)
	}
}

// TestAppendCaller_EscapesTheFileName pins that the caller member stays valid
// JSON whatever the file is called: the name is escaped like any string, and
// either path separator ends the directory part.
func TestAppendCaller_EscapesTheFileName(t *testing.T) {
	cases := map[string]string{
		"/src/app/main.go":   `,"caller":"main.go:7"`,
		`C:\src\app\main.go`: `,"caller":"main.go:7"`,
		`/src/app/we"ird.go`: `,"caller":"we\"ird.go:7"`,
		"/src/app/tab\t.go":  `,"caller":"tab\t.go:7"`,
	}
	for file, want := range cases {
		got := string(AppendCaller(nil, file, 7))
		if got != want {
			t.Errorf("AppendCaller(%q) = %s, want %s", file, got, want)
		}
		entry := &types.LogEntry{Level: types.InfoLevel, Message: "m", File: file, Line: 7}
		if line := NewJsonFormatter().Format(entry, nil); !json.Valid(line) {
			t.Errorf("Format with File %q produced invalid JSON: %s", file, line)
		}
	}
}
