//go:build !windows && !race

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

// Allocation contracts run without the race detector, like the others in
// this module: its instrumentation changes allocation counts.

package syslog

import (
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

// Once warm, a Write to a connected collector allocates nothing.
func TestSyslogAdapter_WriteAllocatesNothing(t *testing.T) {
	_, addr := udpCollector(t)
	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "udp", Address: addr, Tag: "app"})
	defer func() { _ = a.Close() }()

	e := &types.LogEntry{Level: types.InfoLevel, Message: "steady state"}
	for range 2 {
		if err := a.Write(e); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if allocs := testing.AllocsPerRun(1000, func() { _ = a.Write(e) }); allocs != 0 {
		t.Fatalf("Write allocates %.2f times, want 0", allocs)
	}
}
