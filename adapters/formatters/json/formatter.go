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

// Package json provides JSON formatting for log entries
// Author: Admilson B. F. Cossa
package json

import (
	"fmt"
	"math"
	"strconv"

	"github.com/go-gen-ecosystem/halolog/types"
)

const (
	// smallIntCacheSize bounds the pre-rendered small-integer string cache. It
	// covers 0..511, which spans the common cases (small counts, ports, and the
	// full HTTP status-code range) so those values format with a single copy
	// instead of a strconv conversion.
	smallIntCacheSize = 512
)

func init() {
	// Pre-compute small ints (correct for any width, e.g. 3-digit codes).
	for i := 0; i < smallIntCacheSize; i++ {
		smallInts[i] = strconv.Itoa(i)
	}

	// A byte can be written into a JSON string verbatim unless it is a control
	// character (< 0x20), a double quote, or a backslash.
	for c := 0; c < 256; c++ {
		jsonNoEscape[c] = c >= 0x20 && c != '"' && c != '\\'
	}
}

// jsonNoEscape[c] reports whether byte c needs no escaping in a JSON string.
// A single indexed load replaces three comparisons per byte on the hot path.
var jsonNoEscape [256]bool

// KeyFragment returns the pre-escaped JSON member prefix for name — the bytes
// `,"<escaped-name>":`. The root Key constructor caches the result on an
// immutable FieldKey so the hot path can emit a pre-declared key with a single
// copy and no lookup.
func KeyFragment(name string) []byte {
	buf := make([]byte, 0, len(name)+4)
	buf = append(buf, ',', '"')
	buf = appendJSONString(buf, name)
	return append(buf, '"', ':')
}

var smallInts [smallIntCacheSize]string

var levelCache = [7]string{
	`,"level":"TRACE"`,
	`,"level":"DEBUG"`,
	`,"level":"INFO"`,
	`,"level":"WARN"`,
	`,"level":"ERROR"`,
	`,"level":"FATAL"`,
	`,"level":"PANIC"`,
}

// Formatter is a zero-allocation formatter that renders log entries as JSON.
// Its timestamp precision is fixed at construction: the chosen strategy is
// stored as a function value, so the hot path never branches on precision.
type Formatter struct {
	appendHeader headerAppender
	_            [56]byte // Pad to a cache line
}

// NewJsonFormatter creates a JSON formatter with second-precision timestamps
// (the fastest configuration — headers are served from the fused cache).
func NewJsonFormatter() *Formatter {
	return NewJsonFormatterWithPrecision(PrecisionSecond)
}

// NewJsonFormatterWithPrecision creates a JSON formatter whose "time" field
// carries the given fractional resolution. Out-of-range values clamp to
// PrecisionNano.
func NewJsonFormatterWithPrecision(p TimePrecision) *Formatter {
	if p >= precisionCount {
		p = PrecisionNano
	}
	return &Formatter{appendHeader: headerAppenders[p]}
}

// Format appends the JSON encoding of entry to dst and returns the extended slice.
//
//go:noinline
func (f *Formatter) Format(entry *types.LogEntry, dst []byte) []byte {
	if entry == nil {
		return dst
	}

	// Header (`{"time":..,"level":..,"message":..`) — the exact same rendering
	// the direct-append fast path uses, so both paths stay byte-identical.
	dst = appendHeaderWith(dst, f.appendHeader, entryUnixNanos(entry), entry.Level, entry.Message)

	// Caller — the same member the direct path copies from core's call-site
	// cache, so both paths stay byte-identical.
	if entry.Line >= 0 && entry.File != "" {
		dst = AppendCaller(dst, entry.File, entry.Line)
	}

	// Fields — WithField stores into StaticFields (the fast path); the dynamic
	// Fields slice holds overflow and map-style fields. Both must be emitted.
	n := entry.StaticFieldCount
	if n > len(entry.StaticFields) {
		n = len(entry.StaticFields)
	}
	for i := 0; i < n; i++ {
		dst = appendField(dst, &entry.StaticFields[i])
	}
	for i := range entry.Fields {
		dst = appendField(dst, &entry.Fields[i])
	}

	return append(dst, '}', '\n')
}

// AppendCaller appends the `,"caller":"file.go:42"` member: the base name of
// file (either path separator) and the line. The name is escaped like any JSON
// string, so an unusual file name cannot break the line.
func AppendCaller(dst []byte, file string, line int) []byte {
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '/' || file[i] == '\\' {
			file = file[i+1:]
			break
		}
	}
	dst = append(dst, `,"caller":"`...)
	dst = appendJSONString(dst, file)
	dst = append(dst, ':')
	dst = appendInt(dst, int64(line))
	return append(dst, '"')
}

// appendField appends `,"key":value` for a single field, emitting the value with
// its correct JSON type (numbers and bools unquoted). It prefers the
// boxing-free typed Val; when Val is unset (KindUnknown, as produced by the
// interface-based WithField API) it falls back to the legacy Value interface{}.
func appendField(dst []byte, field *types.TypedFieldData) []byte {
	dst = appendKeyPrefix(dst, field.KeyDesc, field.Key)
	return appendValue(dst, &field.Val, field.Value)
}

// appendKeyPrefix emits the `,"key":` member prefix. A pre-declared FieldKey
// carries its own pre-escaped fragment and is emitted with a single copy (no
// escaping). A plain string key is scanned and copied in ONE fused pass — for
// the short keys typical of logging this beats the scan-then-bulk-copy shape
// (profiled at ~25% of a ten-field line). A hostile byte bails to the exact
// escaping path from the key's start, so output is byte-identical either way.
func appendKeyPrefix(dst []byte, kd *types.FieldKey, key string) []byte {
	if kd != nil && len(kd.JSONFragment) > 0 {
		return append(dst, kd.JSONFragment...)
	}
	dst = append(dst, ',', '"')
	mark := len(dst)
	for i := 0; i < len(key); i++ {
		c := key[i]
		if jsonNoEscape[c] {
			dst = append(dst, c)
			continue
		}
		return append(appendJSONEscaped(dst[:mark], key, i), '"', ':')
	}
	return append(dst, '"', ':')
}

// appendValue emits the JSON encoding of a field value. legacy carries the
// interface value for the KindUnknown/WithField path. The value is taken by
// pointer to avoid copying the FieldValue on the hot path.
func appendValue(dst []byte, v *types.FieldValue, legacy interface{}) []byte {
	switch v.Kind {
	case types.KindString:
		dst = append(dst, '"')
		dst = appendJSONString(dst, v.String)
		return append(dst, '"')
	case types.KindInt, types.KindInt64:
		return appendInt(dst, v.Int64)
	case types.KindFloat64:
		return appendFloat(dst, v.Float64, 64)
	case types.KindBool:
		if v.Int64 != 0 {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	case types.KindError:
		dst = append(dst, '"')
		if v.String != "" {
			dst = appendJSONString(dst, v.String)
		} else if e, ok := v.Any.(error); ok && e != nil {
			dst = appendJSONString(dst, e.Error())
		}
		return append(dst, '"')
	case types.KindAny:
		return appendAny(dst, v.Any)
	default: // KindUnknown → legacy interface value
		return appendAny(dst, legacy)
	}
}

// appendAny renders an interface{} value as a correctly-typed JSON value.
// Common scalar types stay allocation-free; only genuinely unknown types fall
// back to fmt (a rare path).
func appendAny(dst []byte, v interface{}) []byte {
	switch x := v.(type) {
	case nil:
		return append(dst, "null"...)
	case string:
		dst = append(dst, '"')
		dst = appendJSONString(dst, x)
		return append(dst, '"')
	case bool:
		if x {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	case int:
		return appendInt(dst, int64(x))
	case int8:
		return appendInt(dst, int64(x))
	case int16:
		return appendInt(dst, int64(x))
	case int32:
		return appendInt(dst, int64(x))
	case int64:
		return appendInt(dst, x)
	case uint:
		return appendUint(dst, uint64(x))
	case uint8:
		return appendUint(dst, uint64(x))
	case uint16:
		return appendUint(dst, uint64(x))
	case uint32:
		return appendUint(dst, uint64(x))
	case uint64:
		return appendUint(dst, x)
	case float32:
		return appendFloat(dst, float64(x), 32)
	case float64:
		return appendFloat(dst, x, 64)
	case error:
		dst = append(dst, '"')
		dst = appendJSONString(dst, x.Error())
		return append(dst, '"')
	default:
		dst = append(dst, '"')
		dst = appendJSONString(dst, fmt.Sprintf("%v", x))
		return append(dst, '"')
	}
}

// SWAR constants for word-at-a-time escape detection.
const (
	swarOnes  = 0x0101010101010101
	swarHighs = 0x8080808080808080
)

// jsonEscapeMask reports (as a non-zero value) whether any byte of the
// little-endian word w needs JSON escaping: below 0x20, '"' (0x22), or
// '\' (0x5C). It uses the classic branch-free "hasless"/"haszero" word
// tricks; their any-byte detection is exact (per-lane bits may over-report
// on borrow propagation, which is harmless — a hit only routes the string
// to the precise per-byte escape path). Bytes ≥ 0x80 (UTF-8 continuation
// and lead bytes) are never flagged by the hasless term because their own
// high bit clears the &^w factor.
func jsonEscapeMask(w uint64) uint64 {
	below := (w - swarOnes*0x20) &^ w & swarHighs
	q := w ^ (swarOnes * '"')
	quote := (q - swarOnes) &^ q & swarHighs
	b := w ^ (swarOnes * '\\')
	backslash := (b - swarOnes) &^ b & swarHighs
	return below | quote | backslash
}

// appendJSONString appends s to dst as a JSON string body (no surrounding
// quotes), escaping only where required. The clean-string common case scans
// eight bytes per iteration: the manual shift-OR load below is recognized by
// the compiler and fused into a single 8-byte load on little-endian
// architectures, so the scan is one load plus a handful of ALU ops per word
// instead of eight table lookups. Any word containing an escape-worthy byte
// (and the sub-word tail) falls back to the exact per-byte path, keeping the
// output byte-identical to the previous implementation.
func appendJSONString(dst []byte, s string) []byte {
	i, n := 0, len(s)
	for ; i+8 <= n; i += 8 {
		w := uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
			uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
		if jsonEscapeMask(w) != 0 {
			// A dirty byte lies in [i, i+8); the escape path re-scans from i
			// per byte, emitting clean bytes verbatim.
			return appendJSONEscaped(dst, s, i)
		}
	}
	for ; i < n; i++ {
		if !jsonNoEscape[s[i]] {
			return appendJSONEscaped(dst, s, i)
		}
	}
	return append(dst, s...)
}

//go:noinline
func appendJSONEscaped(dst []byte, s string, start int) []byte {
	// Pre-allocate for the worst case: a control character expands to \u00XX
	// (6 bytes per input byte). Under-sizing here is only a perf hazard —
	// append still grows — but sizing correctly avoids a second copy.
	remaining := s[start:]
	if cap(dst)-len(dst) < len(remaining)*6 {
		newDst := make([]byte, len(dst), len(dst)+len(remaining)*6)
		copy(newDst, dst)
		dst = newDst
	}

	// Copy safe prefix
	dst = append(dst, s[:start]...)

	// High-performance escaping using jump table approach
	// Order by frequency: \\, \", \n, \t, \r, then control chars
	for i := 0; i < len(remaining); i++ {
		c := remaining[i]
		switch {
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hexDigit(c>>4), hexDigit(c&0xF))
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}

func appendInt(dst []byte, i int64) []byte {
	if i >= 0 && i < int64(smallIntCacheSize) {
		return append(dst, smallInts[i]...)
	}
	return strconv.AppendInt(dst, i, 10)
}

// appendFloat renders a float as a JSON value. Non-finite values (NaN, ±Inf)
// have no JSON number representation — strconv would emit bare NaN/+Inf and
// corrupt the line — so they are emitted as quoted strings, matching zerolog.
func appendFloat(dst []byte, f float64, bits int) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, `"NaN"`...)
	case math.IsInf(f, 1):
		return append(dst, `"+Inf"`...)
	case math.IsInf(f, -1):
		return append(dst, `"-Inf"`...)
	}
	return strconv.AppendFloat(dst, f, 'g', -1, bits)
}

func appendUint(dst []byte, u uint64) []byte {
	if u < uint64(smallIntCacheSize) {
		return append(dst, smallInts[u]...)
	}
	return strconv.AppendUint(dst, u, 10)
}

// EstimatedSize returns an estimated buffer size for pre-allocation
func (f *Formatter) EstimatedSize() int {
	return 4096 // Default 4KB buffer size
}

// Reset resets any internal state (for pooling)
func (f *Formatter) Reset() {
	// Stateless formatter - no-op
}
