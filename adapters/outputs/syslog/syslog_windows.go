//go:build windows

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
	"errors"

	"github.com/go-gen-ecosystem/halolog/types"
)

// Pre-allocated errors for zero-allocation design
var (
	ErrSyslogNotSupported = errors.New("syslog is not supported on Windows")
)

// SyslogAdapter stub for Windows - returns error on any operation.
// Name kept stable across the unix/windows build pair and existing call sites.
//
//nolint:revive // stable cross-platform public API name; renaming would break the unix build twin and callers
type SyslogAdapter struct {
	tag string
}

// SyslogAdapterOptions stub for Windows.
//
//nolint:revive // stable cross-platform public API name; renaming would break the unix build twin and callers
type SyslogAdapterOptions struct {
	Network   string
	Address   string
	Facility  int // Use int instead of syslog.Priority
	Tag       string
	Formatter types.Formatter
}

// NewSyslogAdapter creates a stub syslog adapter for Windows
func NewSyslogAdapter(tag string) *SyslogAdapter {
	return &SyslogAdapter{
		tag: tag,
	}
}

// NewSyslogAdapterWithOptions creates a stub syslog adapter for Windows
func NewSyslogAdapterWithOptions(options *SyslogAdapterOptions) *SyslogAdapter {
	if options == nil {
		options = &SyslogAdapterOptions{}
	}
	return &SyslogAdapter{
		tag: options.Tag,
	}
}

// Name returns the name of this adapter
func (a *SyslogAdapter) Name() string {
	return "SyslogAdapter"
}

// Write implements the Adapter interface - returns error on Windows
func (a *SyslogAdapter) Write(entry *types.LogEntry) error {
	return ErrSyslogNotSupported
}

// Flush implements the Adapter interface
func (a *SyslogAdapter) Flush() error {
	return nil
}

// Close implements the Adapter interface
func (a *SyslogAdapter) Close() error {
	return nil
}

// SetFormatter implements the Adapter interface (no-op on Windows)
func (a *SyslogAdapter) SetFormatter(formatter types.Formatter) {
	// No-op on Windows
}

// SetFacility sets the syslog facility (no-op on Windows)
func (a *SyslogAdapter) SetFacility(facility int) {
	// No-op on Windows
}

// GetFacility returns the current syslog facility (always 0 on Windows)
func (a *SyslogAdapter) GetFacility() int {
	return 0
}

// SetTag sets the syslog tag
func (a *SyslogAdapter) SetTag(tag string) {
	a.tag = tag
}

// GetTag returns the current syslog tag
func (a *SyslogAdapter) GetTag() string {
	return a.tag
}

// Health implements types.InternalAdapter - returns error on Windows
func (a *SyslogAdapter) Health() error {
	return ErrSyslogNotSupported
}

// WriteZero implements types.InternalAdapter - returns error on Windows
func (a *SyslogAdapter) WriteZero(entry *types.LogEntry) error {
	return ErrSyslogNotSupported
}
