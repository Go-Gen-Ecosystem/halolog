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

// Package otelbridge — OpenTelemetry Logs output adapter.
//
// Bind (bridge.go) correlates a log line by stamping trace_id and span_id
// onto it. Adapter goes the other way: it emits the whole entry into the
// OpenTelemetry Logs API, so the same line reaches an OTLP backend as a
// LogRecord while a console or file adapter keeps writing it to stderr.
// Register both and core.Logger fans out to each in turn:
//
//	prov := otelsdklog.NewLoggerProvider(otelsdklog.WithProcessor(proc))
//	logger := core.New().
//	    Adapters(console.New(), otelbridge.NewAdapter("my/service",
//	        otelbridge.WithLoggerProvider(prov))).
//	    MustBuild()
//
// Author: Admilson B. F. Cossa
package otelbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace"
)

// DefaultAdapterName is the adapter's registry name when WithName is not given.
const DefaultAdapterName = "otel"

// DefaultFlushTimeout is the cooperative drain deadline used by Flush and Close.
// A provider that ignores context cancellation can still block.
const DefaultFlushTimeout = 5 * time.Second

// ErrFlushUnavailable reports an unknown drain target for a global proxy.
// Supply an explicit provider or a callback paired with the emitting provider.
var ErrFlushUnavailable = errors.New("otelbridge: global provider has no identifiable flush target")

// attrStackCap stages narrow entries on the stack. Wider upper bounds reserve
// one heap slice sized from the entry, avoiding geometric append growth.
// Correlation and duplicate fields may reduce the final attribute count.
const attrStackCap = 16

// Attribute keys the adapter derives from entry metadata rather than from a
// logged field. A field of the same name takes precedence — see attributes.
const (
	attrComponent = "component"
	attrError     = "error"
	attrFilePath  = "code.file.path"
	attrLineNo    = "code.line.number"
)

// Bits recording which derived keys a logged field already claimed.
const (
	bitComponent uint8 = 1 << iota
	bitError
	bitFilePath
	bitLineNo
)

// Bits recording which correlation fields were consumed into the record's own
// trace context and so must not be repeated as attributes.
const (
	bitTraceID uint8 = 1 << iota
	bitSpanID
	bitTraceFlags
)

// Adapter emits HaloLog entries as OpenTelemetry log records. It satisfies
// types.Adapter, so it composes with every other output through the logger's
// normal adapter fan-out.
//
// Cost per emitted record, measured against a discarding logger by
// TestAdapter_AllocationBudgets (which holds these as ceilings):
//
//	up to 5 attributes                      0 allocs
//	6–16 plain attributes                  1 alloc
//	17 attributes (past the staging buffer) 2 allocs
//	correlated (Bind)                       2 allocs, for the span context
//
// A severity the SDK drops skips the attribute work entirely, but still pays
// for the span context: Enabled has to be asked under the same correlation
// context Emit would use, or a processor filtering on the sampled flag would
// answer for a record that is not the one being emitted.
//
// A real SDK adds its own cost on top; these are the adapter's, not the
// export pipeline's.
type Adapter struct {
	logger         otellog.Logger
	provider       otellog.LoggerProvider
	name           string
	flushTimeout   time.Duration
	flushFunc      func(context.Context) error
	globalProvider bool
}

// forceFlusher is the drain method a LoggerProvider may offer.
// sdklog.LoggerProvider has it; the API interface does not require it.
type forceFlusher interface {
	ForceFlush(context.Context) error
}

type adapterConfig struct {
	provider     otellog.LoggerProvider
	name         string
	version      string
	schemaURL    string
	flushTimeout time.Duration
	flushFunc    func(context.Context) error
}

// WithFlushFunc supplies the drain operation paired with the emitting provider.
// The callback must honor cancellation and remain paired if the global changes.
func WithFlushFunc(fn func(context.Context) error) Option {
	return func(c *adapterConfig) { c.flushFunc = fn }
}

// Option configures an Adapter.
type Option func(*adapterConfig)

// WithLoggerProvider sets the provider the adapter draws its Logger from.
// Without it the adapter uses the global provider, which delegates — a
// provider installed with global.SetLoggerProvider after the adapter is built
// still receives its records.
func WithLoggerProvider(provider otellog.LoggerProvider) Option {
	return func(c *adapterConfig) {
		if provider != nil {
			c.provider = provider
		}
	}
}

// WithName sets the adapter's registry name, used by AdapterManager lookups.
// Give each Adapter a distinct name when registering more than one.
func WithName(name string) Option {
	return func(c *adapterConfig) {
		if name != "" {
			c.name = name
		}
	}
}

// WithVersion records the instrumented package's version on the emitting scope.
func WithVersion(version string) Option {
	return func(c *adapterConfig) { c.version = version }
}

// WithSchemaURL records the schema URL of the emitting scope.
func WithSchemaURL(url string) Option {
	return func(c *adapterConfig) { c.schemaURL = url }
}

// WithFlushTimeout sets a cooperative drain deadline; it cannot interrupt a
// provider that ignores cancellation. A non-positive duration means no deadline.
func WithFlushTimeout(d time.Duration) Option {
	return func(c *adapterConfig) { c.flushTimeout = d }
}

// NewAdapter returns an Adapter emitting through scopeName, which should be the
// import path of the package doing the logging (the OpenTelemetry instrumentation
// scope convention) — not this bridge's own path.
func NewAdapter(scopeName string, opts ...Option) *Adapter {
	cfg := adapterConfig{name: DefaultAdapterName, flushTimeout: DefaultFlushTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	usesGlobal := cfg.provider == nil
	if usesGlobal {
		cfg.provider = global.GetLoggerProvider()
	}

	logOpts := make([]otellog.LoggerOption, 0, 2)
	if cfg.version != "" {
		logOpts = append(logOpts, otellog.WithInstrumentationVersion(cfg.version))
	}
	if cfg.schemaURL != "" {
		logOpts = append(logOpts, otellog.WithSchemaURL(cfg.schemaURL))
	}
	return &Adapter{
		logger:         cfg.provider.Logger(scopeName, logOpts...),
		provider:       cfg.provider,
		name:           cfg.name,
		flushTimeout:   cfg.flushTimeout,
		flushFunc:      cfg.flushFunc,
		globalProvider: usesGlobal,
	}
}

// Name returns the adapter's registry name.
func (a *Adapter) Name() string { return a.name }

// Write emits entry as a log record.
func (a *Adapter) Write(entry *types.LogEntry) error {
	if entry == nil {
		return nil
	}
	// A LoggerProvider is free to hand back a nil Logger; emitting into it
	// would panic inside the logging call, which is the one place a logger
	// must never take the program down.
	if a.logger == nil {
		return types.ErrAdapterClosed
	}
	ctx, rec, ok := a.build(entry)
	if !ok {
		return nil
	}
	a.logger.Emit(ctx, rec)
	return nil
}

// WriteZero emits entry as a log record. The Record path never renders bytes,
// so there is no cheaper variant to offer here — it is Write.
func (a *Adapter) WriteZero(entry *types.LogEntry) error { return a.Write(entry) }

// Flush drains the provider, when it offers ForceFlush.
//
// This cannot be a no-op. Logger.Fatal writes its line, calls Logger.Flush,
// and exits the process from inside the logging call — so a batching
// processor's queue is the last place the most important line in the program
// can be, and the caller never gets a chance to drain it itself.
//
// WithFlushTimeout (DefaultFlushTimeout otherwise) supplies a cooperative
// deadline. The provider or callback must honor cancellation.
func (a *Adapter) Flush() error {
	flush := a.flushFunc
	if flush == nil {
		if f, ok := a.provider.(forceFlusher); ok {
			flush = f.ForceFlush
		} else if a.globalProvider {
			return ErrFlushUnavailable
		} else {
			return nil // Explicit provider exposes no drain operation.
		}
	}
	if a.flushTimeout <= 0 {
		return flush(context.Background())
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.flushTimeout)
	defer cancel()

	// The error is returned, not swallowed. A callback that ignores cancellation
	// can still block; this API cannot forcibly terminate provider code.
	return flush(ctx)
}

// Close drains the provider but does not shut it down. The provider belongs
// to the caller and may be shared by other loggers, so Shutdown stays theirs
// to call; closing one adapter must not tear down everyone's pipeline.
func (a *Adapter) Close() error { return a.Flush() }

// SetFormatter is a no-op. Records carry structured attributes to the SDK,
// which serializes them; a text/JSON formatter has nothing to do here.
func (a *Adapter) SetFormatter(types.Formatter) {}

// Health reports the adapter healthy whenever it holds a Logger. It says
// nothing about the pipeline behind it: a no-op provider is healthy and
// exports nothing, and export failures surface through the SDK's error
// handler rather than here.
func (a *Adapter) Health() error {
	if a.logger == nil {
		return types.ErrAdapterClosed
	}
	return nil
}

// build converts entry into a record plus the context carrying its span. The
// bool is false when the record should be dropped (the SDK is not sampling
// this severity), which spares the attribute conversion.
//
// Every value copied here is owned by the record before Emit returns: the
// caller's LogEntry goes straight back to the pool.
func (a *Adapter) build(entry *types.LogEntry) (context.Context, otellog.Record, bool) {
	var rec otellog.Record
	sev := severityOf(entry.Level)

	// The span context is rebuilt before the probe, not after, because Enabled
	// and Emit must agree on which records qualify: a processor that filters
	// on the sampled flag answers differently for a bare context than for the
	// one this record will actually carry. Probing cheaply and emitting richly
	// would drop records the pipeline wanted. The cost is that a dropped
	// correlated record still pays for its context — see the budgets.
	ctx, consumed, fieldCount := a.spanContext(entry)

	if !a.logger.Enabled(ctx, otellog.EnabledParameters{Severity: sev}) {
		return ctx, rec, false
	}

	// ObservedTimestamp is deliberately left unset: the SDK stamps it at Emit,
	// which is a truer observation time than HaloLog's cached clock (up to
	// ~10ms of skew at the default refresh interval).
	if ts := entryTime(entry); !ts.IsZero() {
		rec.SetTimestamp(ts)
	}
	rec.SetSeverity(sev)
	rec.SetSeverityText(entry.Level.String())
	rec.SetBody(otellog.StringValue(entry.Message))

	// One AddAttributes call, not one per attribute: past the record's inline
	// capacity each call grows the overflow slice again.
	if capacity := attributeCapacity(entry, fieldCount); capacity != 0 {
		var stack [attrStackCap]otellog.KeyValue
		attributes := stack[:0]
		if capacity > len(stack) {
			// Bound wide-record scratch by the entry shape, not append's
			// geometric growth. The Record still takes its own attribute copy.
			attributes = make([]otellog.KeyValue, 0, capacity)
		}
		rec.AddAttributes(a.attributes(attributes, entry, consumed)...)
	}
	return ctx, rec, true
}

// attributeCapacity adds metadata to the bound from the existing correlation
// traversal. This O(1) step adds no indexed-store lock or extra field traversal.
// Duplicates and consumed correlation can reduce the actual attribute count.
// It never evaluates user Error methods or converts field values.
func attributeCapacity(entry *types.LogEntry, n int) int {
	if entry.Component != "" {
		n++
	}
	if entry.ErrorMsg != "" || entry.Error != nil {
		n++
	}
	if entry.File != "" && entry.Line >= 0 {
		n += 2
	}
	return n
}

// entryTime resolves the entry's timestamp the way the JSON formatter's
// entryUnixNanos does — the hot path writes TimestampUnix and may leave the
// wall-clock Timestamp zero, so preferring the other order dates records to
// the zero time. A zero result means the entry carried no timestamp at all
// and the SDK should stamp its own.
func entryTime(entry *types.LogEntry) time.Time {
	if entry.TimestampUnix != 0 {
		return time.Unix(0, entry.TimestampUnix)
	}
	return entry.Timestamp
}

// spanContext rebuilds the span the entry was logged under, reports which
// correlation fields it consumed, and counts fields for bounded staging in the
// same traversal. The adapter interface passes no
// context.Context, so the only trace information available is what Bind
// already stamped onto the entry as fields — re-parsing those hex strings is
// what puts a real TraceID on the record instead of a pair of attributes the
// backend cannot correlate on.
//
// A valid trace id is the whole requirement. The data model allows a record
// that names its trace without naming a span, and the SDK copies the trace
// context out of ctx without checking validity, so a trace id whose span id is
// missing or malformed still correlates — the log lands on the trace, just not
// on a span. A span id alone is meaningless and does not correlate.
//
// A correlation key that never parsed is reported unconsumed and stays in the
// attributes, visible to whoever has to debug it rather than silently dropped.
// Consumption is tracked per key. The final occurrence is authoritative even
// when invalid or redacted: it clears earlier data and remains an attribute.
func (a *Adapter) spanContext(entry *types.LogEntry) (context.Context, uint8, int) {
	var (
		cfg        trace.SpanContextConfig
		consumed   uint8
		fieldCount int
	)
	forEachField(entry, func(key string, f types.TypedFieldData) {
		fieldCount++
		// Match the key before touching the value: every other field on the
		// entry would otherwise be converted here only to be discarded.
		var bit uint8
		switch key {
		case keyTraceID.Name:
			bit = bitTraceID
		case keySpanID.Name:
			bit = bitSpanID
		case keyTraceFlags.Name:
			bit = bitTraceFlags
		default:
			return
		}

		// Clear before parsing. A final redaction, malformed value or wrong
		// type must not leave an earlier valid ID/flag on the emitted record.
		consumed &^= bit
		switch bit {
		case bitTraceID:
			cfg.TraceID = trace.TraceID{}
		case bitSpanID:
			cfg.SpanID = trace.SpanID{}
		case bitTraceFlags:
			cfg.TraceFlags = 0
		}

		hex, ok := stringOf(f)
		if !ok {
			return
		}
		switch bit {
		case bitTraceID:
			if id, err := trace.TraceIDFromHex(hex); err == nil {
				cfg.TraceID, consumed = id, consumed|bitTraceID
			}
		case bitSpanID:
			if id, err := trace.SpanIDFromHex(hex); err == nil {
				cfg.SpanID, consumed = id, consumed|bitSpanID
			}
		case bitTraceFlags:
			if v, err := strconv.ParseUint(hex, 16, 8); err == nil {
				cfg.TraceFlags, consumed = trace.TraceFlags(v), consumed|bitTraceFlags
			}
		}
	})

	if consumed&bitTraceID == 0 {
		return context.Background(), 0, fieldCount
	}
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(cfg)), consumed, fieldCount
}

// attributes appends the entry's fields, context, component, error, and source
// location to dst.
//
// The correlation fields consumed into the record's trace context are skipped:
// they are already its TraceID and SpanID, and re-emitting them as attributes
// would have the backend index the same value twice. For unconsumed correlation
// keys, only the final occurrence stays: stale values must not cross Emit before
// an SDK can deduplicate them. One that did not parse
// was not consumed, and stays.
//
// A logged field wins over the metadata the adapter would derive under the
// same key — a field named "component" or "error" is the more specific value,
// and emitting both would put two attributes with one key on the record, which
// the data model leaves undefined. The guard covers the derived keys only:
// two logged fields sharing a key are passed through as the caller wrote them,
// and the SDK deduplicates. Fields with no key at all are dropped, because an
// empty attribute key is not valid.
//
// Neither GetAllFields nor GetAllContext is used — both build a merged slice
// per call, which is an allocation this path can avoid by walking the three
// storage forms directly.
func (a *Adapter) attributes(dst []otellog.KeyValue, entry *types.LogEntry, consumed uint8) []otellog.KeyValue {
	var claimed uint8
	// Fixed-size, one-based indexes avoid a map and another field traversal.
	// Zero means no attribute has been appended for that correlation key.
	var correlationSlots [3]int

	add := func(key string, f types.TypedFieldData) {
		if key == "" {
			return
		}
		correlationSlot := -1
		switch key {
		case keyTraceID.Name:
			if consumed&bitTraceID != 0 {
				return
			}
			correlationSlot = 0
		case keySpanID.Name:
			if consumed&bitSpanID != 0 {
				return
			}
			correlationSlot = 1
		case keyTraceFlags.Name:
			if consumed&bitTraceFlags != 0 {
				return
			}
			correlationSlot = 2
		}
		claimed |= derivedKeyBit(key)
		kv := otellog.KeyValue{Key: key, Value: logValue(f)}
		if correlationSlot >= 0 {
			if slot := correlationSlots[correlationSlot]; slot != 0 {
				dst[slot-1] = kv
				return
			}
			correlationSlots[correlationSlot] = len(dst) + 1
		}
		dst = append(dst, kv)
	}

	forEachField(entry, add)

	if entry.Component != "" && claimed&bitComponent == 0 {
		dst = append(dst, otellog.String(attrComponent, entry.Component))
	}
	if claimed&bitError == 0 {
		// Checked before deriving: errorMessage may call Error(), whose result
		// would be thrown away when a logged field already holds the key.
		if msg := errorMessage(entry); msg != "" {
			dst = append(dst, otellog.String(attrError, msg))
		}
	}
	// The JSON formatter suppresses the caller unless Line >= 0; match it
	// rather than exporting a line number no source file has.
	if entry.File != "" && entry.Line >= 0 {
		if claimed&bitFilePath == 0 {
			dst = append(dst, otellog.String(attrFilePath, entry.File))
		}
		if claimed&bitLineNo == 0 {
			dst = append(dst, otellog.Int(attrLineNo, entry.Line))
		}
	}
	return dst
}

// derivedKeyBit marks a logged field as having claimed one of the keys the
// adapter would otherwise derive from entry metadata.
func derivedKeyBit(key string) uint8 {
	switch key {
	case attrComponent:
		return bitComponent
	case attrError:
		return bitError
	case attrFilePath:
		return bitFilePath
	case attrLineNo:
		return bitLineNo
	default:
		return 0
	}
}

// forEachField walks every field the entry carries — static, dynamic, indexed,
// and context — in the order a formatter would, without the merged slices
// GetAllFields and GetAllContext allocate.
//
// Correlation and attributes share this one iterator deliberately: walking
// different subsets meant a trace_id in context storage was emitted as a plain
// attribute and never became the record's TraceID.
func forEachField(entry *types.LogEntry, fn func(key string, f types.TypedFieldData)) {
	for i := 0; i < entry.StaticFieldCount && i < len(entry.StaticFields); i++ {
		f := entry.StaticFields[i]
		fn(fieldName(f), f)
	}
	for _, f := range entry.Fields {
		fn(fieldName(f), f)
	}
	for i := 0; i < entry.StaticContextCount && i < len(entry.StaticContext); i++ {
		f := entry.StaticContext[i]
		fn(fieldName(f), f)
	}
	for _, f := range entry.Context {
		fn(fieldName(f), f)
	}
	// Iterate, not GetAll: the snapshot helper appends into a fresh slice on
	// every call. Indexed values are always strings.
	if entry.IndexedStore != nil {
		entry.IndexedStore.Iterate(func(key, value string) {
			fn(key, types.TypedFieldData{Key: key, Val: types.StringValue(value)})
		})
	}
}

// stringOf reads a field as a string through logValue, so correlation sees
// exactly the value the record would carry — whichever storage form holds it,
// and including a masker's rewrite.
func stringOf(f types.TypedFieldData) (string, bool) {
	v := logValue(f)
	if v.Kind() != otellog.KindString {
		return "", false
	}
	return v.AsString(), true
}

// fieldName resolves a field's key from either the plain string or the
// pre-declared descriptor the keyed builders carry.
func fieldName(f types.TypedFieldData) string {
	if f.Key != "" {
		return f.Key
	}
	if f.KeyDesc != nil {
		return f.KeyDesc.Name
	}
	return ""
}

// errorMessage prefers the pre-rendered string so the adapter never calls
// Error() on the hot path when the entry already did.
func errorMessage(entry *types.LogEntry) string {
	if entry.ErrorMsg != "" {
		return entry.ErrorMsg
	}
	if entry.Error != nil {
		return entry.Error.Error()
	}
	return ""
}

// logValue converts a HaloLog field to its OpenTelemetry equivalent.
//
// A non-nil Value alongside typed storage means something rewrote the field
// after the builder set it, and masking is the only thing in the pipeline that
// does. The rewrite wins: preferring the typed original here would export the
// value the masker had just replaced, which on this boundary means shipping
// the PII off the host.
func logValue(f types.TypedFieldData) otellog.Value {
	if f.Val.Kind != types.KindUnknown && f.Value != nil {
		return anyValue(f.Value)
	}
	switch f.Val.Kind {
	case types.KindString, types.KindError:
		return otellog.StringValue(f.Val.String)
	case types.KindInt, types.KindInt64:
		return otellog.Int64Value(f.Val.Int64)
	case types.KindFloat64:
		return otellog.Float64Value(f.Val.Float64)
	case types.KindBool:
		return otellog.BoolValue(f.Val.Int64 != 0)
	case types.KindAny:
		return anyValue(f.Val.Any)
	case types.KindUnknown:
		return anyValue(f.Value)
	default:
		return anyValue(f.Value)
	}
}

// anyValue converts an interface-boxed value. Every fixed-width integer and
// float narrows to the two widths the log API models (int64, float64), which
// is lossless for all of them except uint64 above math.MaxInt64 — see
// uintValue. Unhandled types render through fmt.Sprint, matching what the JSON
// formatter does with the same value.
func anyValue(v interface{}) otellog.Value {
	switch t := v.(type) {
	case nil:
		return otellog.Value{}
	case string:
		return otellog.StringValue(t)
	case bool:
		return otellog.BoolValue(t)
	case int:
		return otellog.Int64Value(int64(t))
	case int8:
		return otellog.Int64Value(int64(t))
	case int16:
		return otellog.Int64Value(int64(t))
	case int32:
		return otellog.Int64Value(int64(t))
	case int64:
		return otellog.Int64Value(t)
	case uint:
		return uintValue(uint64(t))
	case uint8:
		return otellog.Int64Value(int64(t))
	case uint16:
		return otellog.Int64Value(int64(t))
	case uint32:
		return otellog.Int64Value(int64(t))
	case uint64:
		return uintValue(t)
	case uintptr:
		return uintValue(uint64(t))
	case float32:
		return otellog.Float64Value(float64(t))
	case float64:
		return otellog.Float64Value(t)
	case []byte:
		return bytesValue(t)
	case error:
		return otellog.StringValue(t.Error())
	default:
		return otellog.StringValue(fmt.Sprint(t))
	}
}

// uintValue keeps unsigned values exact. The log API has no unsigned integer
// kind, so anything above math.MaxInt64 would wrap to a negative number if it
// were cast; those emit as an exact decimal string instead. The type of an
// attribute therefore depends on its value near the top of the uint64 range —
// deliberate, and the alternative is silently wrong numbers.
func uintValue(v uint64) otellog.Value {
	if v > math.MaxInt64 {
		return otellog.StringValue(strconv.FormatUint(v, 10))
	}
	return otellog.Int64Value(int64(v))
}

// bytesValue copies the payload. log.BytesValue keeps a pointer to the
// caller's array rather than copying it, so without this a caller reusing its
// buffer after the logging call would rewrite an already-emitted record — and
// records outlive the call, both in the SDK's batch queue and in this
// adapter's contract that nothing survives into the pooled LogEntry. One
// allocation per []byte attribute is the price of that guarantee.
func bytesValue(v []byte) otellog.Value {
	if v == nil {
		return otellog.Value{}
	}
	owned := make([]byte, len(v))
	copy(owned, v)
	return otellog.BytesValue(owned)
}

// severityOf maps HaloLog levels onto the OpenTelemetry severity scale. Panic
// sits one step above Fatal: both are terminal, but the distinction survives
// the round trip to the backend.
func severityOf(level types.LogLevel) otellog.Severity {
	switch level {
	case types.TraceLevel:
		return otellog.SeverityTrace1
	case types.DebugLevel:
		return otellog.SeverityDebug1
	case types.InfoLevel:
		return otellog.SeverityInfo1
	case types.WarnLevel:
		return otellog.SeverityWarn1
	case types.ErrorLevel:
		return otellog.SeverityError1
	case types.FatalLevel:
		return otellog.SeverityFatal1
	case types.PanicLevel:
		return otellog.SeverityFatal2
	default:
		return otellog.SeverityUndefined
	}
}
