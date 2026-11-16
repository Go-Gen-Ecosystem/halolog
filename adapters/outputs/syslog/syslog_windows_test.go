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

package syslog

import (
	"strings"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
	"github.com/go-gen-ecosystem/halolog/utils"
)

func TestSyslogWindowsAdapter_Basic(t *testing.T) {
	adapter := NewSyslogAdapter("test")
	if adapter == nil {
		t.Fatal("NewSyslogAdapter returned nil")
	}

	// Test basic properties
	if adapter.Name() == "" {
		t.Error("SyslogWindows adapter should have a name")
	}

	// Test that adapter can be closed
	err := adapter.Close()
	if err != nil {
		t.Errorf("Close should not error: %v", err)
	}

	// Test that adapter can be flushed
	err = adapter.Flush()
	if err != nil {
		t.Errorf("Flush should not error: %v", err)
	}
}

func TestSyslogWindowsAdapter_NewSyslogAdapterWithOptions(t *testing.T) {
	options := &SyslogAdapterOptions{
		Tag: "test-tag",
	}

	adapter := NewSyslogAdapterWithOptions(options)
	if adapter == nil {
		t.Fatal("NewSyslogAdapterWithOptions returned nil")
	}

	// Test that tag is set correctly
	if adapter.GetTag() != "test-tag" {
		t.Errorf("Expected tag 'test-tag', got '%s'", adapter.GetTag())
	}

	// Test name
	if adapter.Name() != "SyslogAdapter" {
		t.Errorf("Expected name 'SyslogAdapter', got '%s'", adapter.Name())
	}
}

func TestSyslogWindowsAdapter_Write(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	entry := &types.LogEntry{
		Level:     types.InfoLevel,
		Message:   "Test message",
		Timestamp: time.Now(),
		Fields:    []types.TypedFieldData{types.String("key", "value")},
	}

	err := adapter.Write(entry)
	if err == nil {
		t.Error("Expected error when writing on Windows")
	}
	if !strings.Contains(err.Error(), "syslog is not supported on Windows") {
		t.Errorf("Expected specific error message, got: %v", err)
	}
}

func TestSyslogWindowsAdapter_WriteNilEntry(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	err := adapter.Write(nil)
	if err == nil {
		t.Error("Expected error when writing nil entry")
	}
	if !strings.Contains(err.Error(), "syslog is not supported on Windows") {
		t.Errorf("Expected specific error message, got: %v", err)
	}
}

func TestSyslogWindowsAdapter_SetFormatter(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Should not panic
	adapter.SetFormatter(nil)
	t.Log("Formatter set successfully (no-op on Windows)")
}

func TestSyslogWindowsAdapter_SetFacility(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Should not panic
	adapter.SetFacility(16) // local0 facility
	t.Log("Facility set successfully (no-op on Windows)")
}

func TestSyslogWindowsAdapter_GetFacility(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Should always return 0 on Windows
	facility := adapter.GetFacility()
	if facility != 0 {
		t.Errorf("Expected facility 0 on Windows, got %d", facility)
	}
}

func TestSyslogWindowsAdapter_SetTag(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Set new tag
	adapter.SetTag("new-tag")

	// Verify tag was updated
	if adapter.GetTag() != "new-tag" {
		t.Errorf("Expected tag 'new-tag', got '%s'", adapter.GetTag())
	}
}

func TestSyslogWindowsAdapter_GetTag(t *testing.T) {
	adapter := NewSyslogAdapter("initial-tag")

	// Test initial tag
	if adapter.GetTag() != "initial-tag" {
		t.Errorf("Expected tag 'initial-tag', got '%s'", adapter.GetTag())
	}

	// Test after setting new tag
	adapter.SetTag("updated-tag")
	if adapter.GetTag() != "updated-tag" {
		t.Errorf("Expected tag 'updated-tag', got '%s'", adapter.GetTag())
	}
}

func TestSyslogWindowsAdapter_Flush(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Flush should not error on Windows
	err := adapter.Flush()
	if err != nil {
		t.Errorf("Flush should not error: %v", err)
	}
}

func TestSyslogWindowsAdapter_Close(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Close should not error on Windows
	err := adapter.Close()
	if err != nil {
		t.Errorf("Close should not error: %v", err)
	}
}

func TestSyslogWindowsAdapter_MultipleOperations(t *testing.T) {
	adapter := NewSyslogAdapter("test")

	// Test multiple write operations
	for i := 0; i < 5; i++ {
		entry := &types.LogEntry{
			Level:     types.InfoLevel,
			Message:   utils.FormatIntWithPrefix("Test message", i),
			Timestamp: time.Now(),
		}

		err := adapter.Write(entry)
		if err == nil {
			t.Error("Expected error for all write operations on Windows")
		}
	}

	// Test flush and close after writes
	err := adapter.Flush()
	if err != nil {
		t.Errorf("Flush should not error: %v", err)
	}

	err = adapter.Close()
	if err != nil {
		t.Errorf("Close should not error: %v", err)
	}
}

// Nil options are refused without a panic.
func TestSyslogWindowsAdapter_NilOptions(t *testing.T) {
	if a := NewSyslogAdapterWithOptions(nil); a == nil || a.Close() != nil {
		t.Fatal("NewSyslogAdapterWithOptions(nil) did not return a usable adapter")
	}
}
