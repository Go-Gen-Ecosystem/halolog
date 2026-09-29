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

// Package core provides the high-performance HaloLog logger implementation.
// Author: Admilson B. F. Cossa

package core

import (
	"fmt"

	"github.com/go-gen-ecosystem/halolog/adapters/outputs/discard"
	"github.com/go-gen-ecosystem/halolog/types"
)

// Builder provides a fluent API for building a configured Logger.
// This is the entry point for all logger configuration.
//
// Example:
//
//	logger := core.New().
//	    Component("my-service").
//	    Level(types.InfoLevel).
//	    Adapter(myAdapter).
//	    Masking(masker).
//	    Metrics().
//	    Build()
type Builder struct {
	config Config
	errors []error
}

// New creates a new Builder with default configuration.
func New() *Builder {
	return &Builder{
		config: Config{
			Level: types.InfoLevel,
		},
	}
}

// Component sets the logger component name.
func (b *Builder) Component(name string) *Builder {
	b.config.Component = name
	return b
}

// Level sets the minimum log level.
func (b *Builder) Level(level types.LogLevel) *Builder {
	b.config.Level = level
	return b
}

// Debug is a convenience method for Level(types.DebugLevel).
func (b *Builder) Debug() *Builder {
	return b.Level(types.DebugLevel)
}

// Info is a convenience method for Level(types.InfoLevel).
func (b *Builder) Info() *Builder {
	return b.Level(types.InfoLevel)
}

// Warn is a convenience method for Level(types.WarnLevel).
func (b *Builder) Warn() *Builder {
	return b.Level(types.WarnLevel)
}

// Error is a convenience method for Level(types.ErrorLevel).
func (b *Builder) Error() *Builder {
	return b.Level(types.ErrorLevel)
}

// Adapter adds an adapter to the logger.
func (b *Builder) Adapter(adapter types.Adapter) *Builder {
	b.config.Adapters = append(b.config.Adapters, adapter)
	return b
}

// Adapters adds multiple adapters to the logger.
func (b *Builder) Adapters(adapters ...types.Adapter) *Builder {
	b.config.Adapters = append(b.config.Adapters, adapters...)
	return b
}

// Discard adds a discard adapter (for testing/benchmarking).
func (b *Builder) Discard() *Builder {
	return b.Adapter(discard.New())
}

// Masking enables PII masking with the provided masker.
func (b *Builder) Masking(masker types.PIIMasker) *Builder {
	b.config.EnableMasking = true
	b.config.Masker = masker
	return b
}

// Sampling enables log sampling with the provided sampler.
func (b *Builder) Sampling(sampler types.Sampler) *Builder {
	b.config.EnableSampling = true
	b.config.Sampler = sampler
	return b
}

// Metrics enables logging metrics collection.
func (b *Builder) Metrics() *Builder {
	b.config.EnableMetrics = true
	return b
}

// Alerts enables alert functionality.
func (b *Builder) Alerts() *Builder {
	b.config.EnableAlerts = true
	return b
}

// Aggregation enables log aggregation.
func (b *Builder) Aggregation() *Builder {
	b.config.EnableAggregation = true
	return b
}

// Caller records where each line was logged, as the member
// "caller":"file.go:42". Off by default: it adds one stack walk per line.
// Once a call site has logged, its lines allocate nothing, for up to 4096
// call sites per process; a site past that is resolved on each line.
func (b *Builder) Caller() *Builder {
	b.config.EnableCaller = true
	return b
}

// CallerSkip records the call site skip frames further up, for applications
// that log through their own helper functions. It must be within [0, 64];
// Build reports anything else.
func (b *Builder) CallerSkip(skip int) *Builder {
	if skip < 0 || skip > maxCallerSkip {
		b.errors = append(b.errors, fmt.Errorf("core: CallerSkip(%d) is outside [0, %d]", skip, maxCallerSkip))
		return b
	}
	b.config.CallerSkip = skip
	return b
}

// Build creates the configured Logger.
// Returns an error if configuration is invalid.
func (b *Builder) Build() (*Logger, error) {
	// Validate configuration
	if len(b.errors) > 0 {
		return nil, b.errors[0]
	}

	// If no adapters configured, use discard
	if len(b.config.Adapters) == 0 {
		b.config.Adapters = append(b.config.Adapters, discard.New())
	}

	return NewLogger(b.config), nil
}

// MustBuild creates the configured Logger or panics.
func (b *Builder) MustBuild() *Logger {
	logger, err := b.Build()
	if err != nil {
		panic(err)
	}
	return logger
}
