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

package file

// Resilience primitives for the file adapter: a circuit breaker around file
// writes and a token-bucket rate limiter on Write.

import (
	"sync/atomic"
	"time"
)

// === Circuit Breaker ===
type circuitBreaker struct {
	state         atomic.Uint32 // 0=closed, 1=half-open, 2=open
	failures      atomic.Int64
	successes     atomic.Int64
	lastFailTime  atomic.Int64
	lastStateTime atomic.Int64

	threshold     int64
	timeout       time.Duration
	halfOpenMax   int64
	halfOpenCount atomic.Int64
}

func newCircuitBreaker(threshold int, timeout time.Duration) *circuitBreaker {
	return &circuitBreaker{
		threshold:   int64(threshold),
		timeout:     timeout,
		halfOpenMax: 3,
	}
}

func (cb *circuitBreaker) Allow() bool {
	state := cb.state.Load()

	switch state {
	case 0: // Closed - allow
		return true

	case 2: // Open - check timeout
		lastFail := time.Unix(0, cb.lastFailTime.Load())
		if time.Since(lastFail) >= cb.timeout {
			// Try to transition to half-open
			cb.state.CompareAndSwap(2, 1)
			cb.lastStateTime.Store(time.Now().UnixNano())
			return true
		}
		return false

	case 1: // Half-open - limited concurrency
		if cb.halfOpenCount.Load() < cb.halfOpenMax {
			cb.halfOpenCount.Add(1)
			return true
		}
		return false

	default:
		return false
	}
}

func (cb *circuitBreaker) RecordSuccess() {
	cb.halfOpenCount.Add(-1)
	cb.successes.Add(1)

	// After 10 successes in half-open, close circuit
	if cb.state.Load() == 1 && cb.successes.Load() >= 10 {
		cb.state.Store(0)
		cb.failures.Store(0)
		cb.successes.Store(0)
	}
}

func (cb *circuitBreaker) RecordFailure() {
	cb.halfOpenCount.Add(-1)
	cb.failures.Add(1)
	cb.lastFailTime.Store(time.Now().UnixNano())

	if cb.failures.Load() >= cb.threshold {
		cb.state.Store(2) // Open circuit
		cb.lastStateTime.Store(time.Now().UnixNano())
	}
}

// === Rate Limiter (Token Bucket) ===
type rateLimiter struct {
	tokens     atomic.Int64
	maxTokens  int64
	refillRate int64 // Tokens per second
	lastRefill atomic.Int64
	enabled    bool
}

func newRateLimiter(tokensPerSec int64, enabled bool) *rateLimiter {
	if !enabled || tokensPerSec <= 0 {
		return &rateLimiter{enabled: false}
	}

	rl := &rateLimiter{
		maxTokens:  tokensPerSec,
		refillRate: tokensPerSec,
		enabled:    true,
	}
	rl.tokens.Store(tokensPerSec)
	rl.lastRefill.Store(time.Now().UnixNano())
	return rl
}

func (rl *rateLimiter) Allow() bool {
	if !rl.enabled {
		return true
	}

	// Try to take token
	for {
		tokens := rl.tokens.Load()
		if tokens > 0 {
			if rl.tokens.CompareAndSwap(tokens, tokens-1) {
				return true
			}
			continue
		}

		// Refill if enough time passed
		now := time.Now().UnixNano()
		last := rl.lastRefill.Load()
		elapsed := time.Duration(now - last)

		if elapsed >= time.Second {
			if rl.lastRefill.CompareAndSwap(last, now) {
				tokensToAdd := rl.refillRate * int64(elapsed.Seconds())
				if tokensToAdd > rl.maxTokens {
					tokensToAdd = rl.maxTokens
				}
				rl.tokens.Store(tokensToAdd)
				continue
			}
		}

		return false
	}
}
