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
// Package config provides configuration management
// Author: Admilson B. F. Cossa

package file

import (
	"time"
)

// RotationConfig defines file rotation and performance settings. A partial
// config is fine: a zero FlushInterval, MaxBatchSize, WriteTimeout,
// QueueSize, CircuitThreshold, or CircuitTimeout takes the value
// DefaultRotationConfig gives it. The other fields keep their zero meaning.
type RotationConfig struct {
	MaxSize          int64         // bytes before the file rotates; 0 never rotates
	MaxAge           time.Duration // backups older than this many whole days are removed; under a day keeps them
	MaxBackups       int           // backups kept; 0 keeps every backup
	Compress         bool          // gzip rotated files
	LocalTime        bool          // currently unused: backup names use local time
	FlushInterval    time.Duration // how often the file is synced to disk
	MaxBatchSize     int           // initial size of the queue buffer, in bytes
	BatchTimeout     time.Duration // unused: the writer wakes as soon as a line is queued
	WriteTimeout     time.Duration // how long Write waits for room in a full queue before dropping the line
	QueueSize        int           // lines that may wait to be written
	UseOSync         bool          // open the file with O_SYNC
	UseFlock         bool          // hold a cross-process lock on the file
	EnableMetrics    bool          // currently unused: metrics are always kept
	EnableBufferPool bool          // currently unused
	CircuitThreshold int           // consecutive write failures that open the circuit breaker
	CircuitTimeout   time.Duration // how long an open circuit drops writes before retrying
	RateLimit        int64         // lines per second; 0 disables the limit
}

// DefaultRotationConfig returns production-ready defaults
func DefaultRotationConfig() *RotationConfig {
	return &RotationConfig{
		MaxSize:          100 * 1024 * 1024,  // 100MB
		MaxAge:           7 * 24 * time.Hour, // 7 days
		MaxBackups:       10,
		Compress:         true,
		LocalTime:        true,
		FlushInterval:    30 * time.Second,
		MaxBatchSize:     64 * 1024,
		BatchTimeout:     100 * time.Millisecond,
		WriteTimeout:     5 * time.Second,
		QueueSize:        65536,
		UseOSync:         false,
		UseFlock:         false,
		EnableMetrics:    true,
		EnableBufferPool: true,
		CircuitThreshold: 10,
		CircuitTimeout:   30 * time.Second,
		RateLimit:        0, // Disabled by default
	}
}

// withDefaults returns a copy of c in which every zero tuning field takes its
// DefaultRotationConfig value. A zero queue size, write timeout, or circuit
// setting cannot work, and partial configs are common, so they are filled in
// rather than used as given. A nil config yields the defaults.
func withDefaults(c *RotationConfig) RotationConfig {
	def := DefaultRotationConfig()
	if c == nil {
		return *def
	}
	out := *c
	out.FlushInterval = orDefault(out.FlushInterval, def.FlushInterval)
	out.MaxBatchSize = orDefault(out.MaxBatchSize, def.MaxBatchSize)
	out.WriteTimeout = orDefault(out.WriteTimeout, def.WriteTimeout)
	out.QueueSize = orDefault(out.QueueSize, def.QueueSize)
	out.CircuitThreshold = orDefault(out.CircuitThreshold, def.CircuitThreshold)
	out.CircuitTimeout = orDefault(out.CircuitTimeout, def.CircuitTimeout)
	return out
}

// orDefault returns v, or def when v is zero or negative.
func orDefault[T ~int | ~int64](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// FileConfig provides high-level configuration for the file adapter.
//
//nolint:revive // exported name intentionally kept for a stable public API; renaming to Config would break importers
type FileConfig struct {
	Path       string
	Rotation   *RotationConfig
	MaxSize    int64
	MaxBackups int
	MaxAge     time.Duration
	Compress   bool
}
