package otelbridge

import (
	"context"
	"testing"

	"github.com/go-gen-ecosystem/halolog/masking"
	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

func adversarialSDKAdapter(t *testing.T) (*Adapter, *memExporter) {
	t.Helper()
	exporter := &memExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return NewAdapter("review/context-adversarial", WithLoggerProvider(provider)), exporter
}

func adversarialMaskOneField(t *testing.T, entry *types.LogEntry, key string) {
	t.Helper()
	masker := masking.NewPIIMasker()
	if err := masker.AddRule(key, "[MASKED]", "field"); err != nil {
		t.Fatal(err)
	}
	// Apply would also run the default token regex against the other hex IDs.
	// Isolate partial masking while still using the real masking rewrite API.
	for i := range entry.Fields {
		if entry.Fields[i].Key == key {
			masker.MaskField(&entry.Fields[i])
			return
		}
	}
	t.Fatalf("fixture has no %s field", key)
}

// Exercise each post-mask correlation component at the real SDK/export boundary.
// In particular, clearing the span must retain a valid trace, while clearing the
// trace must also clear the span and flags on the emitted record.
func TestCorrelationPartialCorrelation(t *testing.T) {
	for _, key := range []string{"trace_id", "span_id", "trace_flags"} {
		for _, change := range []string{"masked", "removed", "wrong_type"} {
			t.Run(key+"/"+change, func(t *testing.T) {
				ctx, entry := requestFixture(37)
				original := trace.SpanContextFromContext(ctx)
				if change == "masked" {
					adversarialMaskOneField(t, entry, key)
				} else {
					for i := range entry.Fields {
						if entry.Fields[i].Key != key {
							continue
						}
						if change == "removed" {
							entry.Fields = append(entry.Fields[:i], entry.Fields[i+1:]...)
						} else {
							entry.Fields[i].Val = types.IntValue(123)
						}
						break
					}
				}
				adapter, exporter := adversarialSDKAdapter(t)
				if err := adapter.Write(entry); err != nil {
					t.Fatal(err)
				}
				got := exporter.only(t)
				wantTrace, wantSpan, wantFlags := original.TraceID(), original.SpanID(), original.TraceFlags()
				switch key {
				case "trace_id":
					wantTrace, wantSpan, wantFlags = trace.TraceID{}, trace.SpanID{}, 0
				case "span_id":
					wantSpan = trace.SpanID{}
				case "trace_flags":
					wantFlags = 0
				}
				if got.TraceID() != wantTrace || got.SpanID() != wantSpan || got.TraceFlags() != wantFlags {
					t.Errorf("exported correlation = %s/%s/%s, want %s/%s/%s", got.TraceID(), got.SpanID(), got.TraceFlags(), wantTrace, wantSpan, wantFlags)
				}
				value, present := recordAttrs(got)[key]
				switch change {
				case "masked":
					if !present || value.AsString() != "[MASKED]" {
						t.Errorf("masked field was not retained: present=%v value=%v", present, value)
					}
				case "removed":
					if present {
						t.Errorf("removed field reappeared as attribute: %v", value)
					}
				case "wrong_type":
					if !present || value.Kind() != otellog.KindInt64 || value.AsInt64() != 123 {
						t.Errorf("unparseable field was not retained: present=%v value=%v", present, value)
					}
				}
			})
		}
	}
}

type adversarialContextLogger struct {
	embedded.Logger
	allow       func(context.Context) bool
	enabledCtx  context.Context
	emittedCtx  context.Context
	enabledCall int
	emitCall    int
}

func (l *adversarialContextLogger) Enabled(ctx context.Context, _ otellog.EnabledParameters) bool {
	l.enabledCtx = ctx
	l.enabledCall++
	return l.allow(ctx)
}

func (l *adversarialContextLogger) Emit(ctx context.Context, _ otellog.Record) {
	l.emittedCtx = ctx
	l.emitCall++
}

func TestCorrelationEnabledEmitConsistency(t *testing.T) {
	for _, change := range []string{"matching", "trace_id", "span_id", "trace_flags", "different_request"} {
		t.Run(change, func(t *testing.T) {
			ctx, entry := requestFixture(38)
			original := trace.SpanContextFromContext(ctx)
			wantTrace, wantSpan, wantFlags := original.TraceID(), original.SpanID(), original.TraceFlags()
			switch change {
			case "trace_id", "span_id", "trace_flags":
				adversarialMaskOneField(t, entry, change)
				switch change {
				case "trace_id":
					wantTrace, wantSpan, wantFlags = trace.TraceID{}, trace.SpanID{}, 0
				case "span_id":
					wantSpan = trace.SpanID{}
				default:
					wantFlags = 0
				}
			case "different_request":
				var other context.Context
				other, entry = requestFixture(39)
				sc := trace.SpanContextFromContext(other)
				wantTrace, wantSpan, wantFlags = sc.TraceID(), sc.SpanID(), sc.TraceFlags()
			}
			probe := &adversarialContextLogger{allow: func(ctx context.Context) bool {
				sc := trace.SpanContextFromContext(ctx)
				return sc.TraceID() == wantTrace && sc.SpanID() == wantSpan && sc.TraceFlags() == wantFlags
			}}
			adapter := &Adapter{logger: probe}
			if err := adapter.Write(entry); err != nil {
				t.Fatal(err)
			}
			if probe.enabledCall != 1 || probe.emitCall != 1 {
				t.Fatalf("expected correlation-dependent admission and emit, got Enabled=%d Emit=%d", probe.enabledCall, probe.emitCall)
			}
			if probe.enabledCtx != probe.emittedCtx || !probe.allow(probe.emittedCtx) {
				t.Fatal("Enabled and Emit received different correlation/context")
			}
		})
	}
}

// Security contract: the last occurrence wins even when redacted or
// otherwise unparseable. Parsing an earlier duplicate must not hide a later
// redaction. The entry fields are the only correlation authority.
func TestCorrelationLastInvalidDuplicateWins(t *testing.T) {
	for _, key := range []string{"trace_id", "span_id", "trace_flags"} {
		for _, storage := range []string{"fields", "context"} {
			for _, final := range []string{"masked", "malformed", "wrong_type"} {
				t.Run(key+"/"+storage+"/"+final, func(t *testing.T) {
					ctx, entry := requestFixture(40)
					original := trace.SpanContextFromContext(ctx)
					redacted := types.TypedFieldData{Key: key, Val: types.StringValue("[MASKED]")}
					wantValue := otellog.StringValue("[MASKED]")
					switch final {
					case "malformed":
						redacted.Val = types.StringValue("not-hex")
						wantValue = otellog.StringValue("not-hex")
					case "wrong_type":
						redacted.Val = types.IntValue(123)
						wantValue = otellog.Int64Value(123)
					}
					if storage == "fields" {
						entry.Fields = append(entry.Fields, redacted)
					} else {
						entry.Context = append(entry.Context, redacted)
					}
					adapter, exporter := adversarialSDKAdapter(t)
					if err := adapter.Write(entry); err != nil {
						t.Fatal(err)
					}
					got := exporter.only(t)
					wantTrace, wantSpan, wantFlags := original.TraceID(), original.SpanID(), original.TraceFlags()
					switch key {
					case "trace_id":
						wantTrace, wantSpan, wantFlags = trace.TraceID{}, trace.SpanID{}, 0
					case "span_id":
						wantSpan = trace.SpanID{}
					case "trace_flags":
						wantFlags = 0
					}
					if got.TraceID() != wantTrace || got.SpanID() != wantSpan || got.TraceFlags() != wantFlags {
						t.Errorf("earlier duplicate survived final %s value: exported %s/%s/%s, want %s/%s/%s", final, got.TraceID(), got.SpanID(), got.TraceFlags(), wantTrace, wantSpan, wantFlags)
					}
					value, present := recordAttrs(got)[key]
					if !present || !value.Equal(wantValue) {
						t.Errorf("final %s value disappeared: present=%v value=%v", final, present, value)
					}
				})
			}
		}
	}
}

func TestCorrelationAPIDuplicateValuesDoNotLeak(t *testing.T) {
	for _, key := range []string{"trace_id", "span_id", "trace_flags"} {
		for _, storage := range []string{"fields", "context"} {
			for _, final := range []string{"masked", "malformed", "wrong_type"} {
				t.Run(key+"/"+storage+"/"+final, func(t *testing.T) {
					_, entry := requestFixture(43)
					last := types.TypedFieldData{Key: key, Val: types.StringValue("[MASKED]")}
					want := otellog.StringValue("[MASKED]")
					switch final {
					case "malformed":
						last.Val, want = types.StringValue("not-hex"), otellog.StringValue("not-hex")
					case "wrong_type":
						last.Val, want = types.IntValue(123), otellog.Int64Value(123)
					}
					if storage == "fields" {
						entry.Fields = append(entry.Fields, last)
					} else {
						entry.Context = append(entry.Context, last)
					}
					capture := &recorder{}
					adapter := NewAdapter("review/context-api-boundary", WithLoggerProvider(capture))
					if err := adapter.Write(entry); err != nil {
						t.Fatal(err)
					}
					// Walk the API Record directly: a map or SDK exporter can hide
					// stale duplicate attributes by selecting only the last one.
					pairs := attrPairs(capture.only(t))
					if n := countKey(pairs, key); n != 1 {
						t.Errorf("API record contains %d %s attributes; want only the final value", n, key)
					}
					for _, pair := range pairs {
						if pair.Key == key && !pair.Value.Equal(want) {
							t.Errorf("stale %s crossed Emit boundary: %v; final value is %v", key, pair.Value, want)
						}
					}
				})
			}
		}
	}
}
