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

package http

import (
	"strconv"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/types"
)

// ZeroJSONFormatter renders one batch entry as
// {"level":..,"message":..,"timestamp":<unix ns>,"fields":{..}}. Strings, keys,
// and values are escaped and typed exactly as the JSON formatter renders them,
// whose helpers it reuses, and it allocates nothing for the field kinds the
// logger produces.
type ZeroJSONFormatter struct{}

// NewZeroJSONFormatter returns the default formatter for HTTP batch entries.
func NewZeroJSONFormatter() types.Formatter {
	return &ZeroJSONFormatter{}
}

// Format renders entry into dst[:0]. A nil entry renders nothing.
func (f *ZeroJSONFormatter) Format(entry *types.LogEntry, dst []byte) []byte {
	if entry == nil {
		return dst[:0]
	}
	return appendEntry(dst[:0], entry)
}

// FormatBatch renders entries as a JSON array into a new slice.
func (f *ZeroJSONFormatter) FormatBatch(entries []types.LogEntry) ([]byte, error) {
	buf := make([]byte, 0, len(entries)*f.EstimatedSize()/8+2)
	buf = append(buf, '[')
	for i := range entries {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendEntry(buf, &entries[i])
	}
	return append(buf, ']'), nil
}

// String returns the formatter name.
func (f *ZeroJSONFormatter) String() string { return "ZeroJSONFormatter" }

// Reset implements types.Formatter; the formatter holds no state.
func (f *ZeroJSONFormatter) Reset() {}

// EstimatedSize returns a pre-allocation hint for one entry.
func (f *ZeroJSONFormatter) EstimatedSize() int { return 2048 }

func appendEntry(dst []byte, entry *types.LogEntry) []byte {
	dst = append(dst, `{"level":"`...)
	dst = append(dst, entry.Level.String()...)
	dst = append(dst, '"')
	if entry.Message != "" {
		dst = jsonfmt.AppendStringField(dst, nil, "message", entry.Message)
	}
	if ts := unixNanos(entry); ts != 0 {
		dst = append(dst, `,"timestamp":`...)
		dst = strconv.AppendInt(dst, ts, 10)
	}

	// The logger stores fields in StaticFields[:StaticFieldCount] and, past
	// that, in Fields; the JSON formatter emits both, and so does this one.
	static := entry.StaticFields[:min(entry.StaticFieldCount, len(entry.StaticFields))]
	if len(static)+len(entry.Fields) > 0 {
		dst = append(dst, `,"fields":`...)
		open := len(dst)
		for i := range static {
			dst = appendField(dst, &static[i])
		}
		for i := range entry.Fields {
			dst = appendField(dst, &entry.Fields[i])
		}
		dst[open] = '{' // every member starts with a comma; the first one opens the object
		dst = append(dst, '}')
	}
	return append(dst, '}')
}

// appendField appends `,"key":value`, preferring the typed value and falling
// back to the interface value set by WithField.
func appendField(dst []byte, field *types.TypedFieldData) []byte {
	v := field.Val
	if v.Kind == types.KindUnknown {
		v = types.FieldValue{Kind: types.KindAny, Any: field.Value}
	}
	return jsonfmt.AppendField(dst, field.KeyDesc, field.Key, v)
}

// unixNanos returns the entry time: the logger sets TimestampUnix, callers
// building entries by hand may set only Timestamp.
func unixNanos(entry *types.LogEntry) int64 {
	if entry.TimestampUnix != 0 {
		return entry.TimestampUnix
	}
	if !entry.Timestamp.IsZero() {
		return entry.Timestamp.UnixNano()
	}
	return 0
}
