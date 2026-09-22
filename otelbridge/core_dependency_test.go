package otelbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/core"
	"github.com/go-gen-ecosystem/halolog/masking"
	"github.com/go-gen-ecosystem/halolog/types"
	"go.opentelemetry.io/otel/trace"
)

// These are consumer contracts, not tests of this checkout's core internals.
// With GOWORK=off and no replacement, they exercise the required module version.
// They must fail, not Skip, when that dependency lacks a security correction.
func TestCoreDependency_MaskingStorage(t *testing.T) {
	for _, placement := range []string{"typed", "boxed", "static", "context", "static_context", "indexed", "descriptor"} {
		t.Run(placement, func(t *testing.T) {
			entry := &types.LogEntry{Level: types.InfoLevel, Message: "test"}
			field := types.TypedFieldData{Key: "password", Val: types.StringValue("synthetic-secret")}
			switch placement {
			case "typed":
				entry.Fields = []types.TypedFieldData{field}
			case "boxed":
				field.Val = types.FieldValue{}
				field.Value = "synthetic-secret"
				entry.Fields = []types.TypedFieldData{field}
			case "static":
				entry.StaticFields = []types.TypedFieldData{field}
				entry.StaticFieldCount = 1
			case "context":
				entry.Context = []types.TypedFieldData{field}
			case "static_context":
				entry.StaticContext = []types.TypedFieldData{field}
				entry.StaticContextCount = 1
			case "indexed":
				entry.EnableIndexedStorage(8)
				entry.AddIndexedField("password", "synthetic-secret")
			case "descriptor":
				field.Key = ""
				field.KeyDesc = &types.FieldKey{Name: "password", JSONFragment: []byte(`,"password":`)}
				entry.Fields = []types.TypedFieldData{field}
			}
			masking.NewPIIMasker().Apply(entry)
			seen := 0
			for _, fields := range [][]types.TypedFieldData{entry.Fields, entry.StaticFields, entry.Context, entry.StaticContext} {
				for _, got := range fields {
					seen++
					if got.Val.String == "synthetic-secret" || got.Value == "synthetic-secret" {
						t.Fatal("required core retains a clear field representation")
					}
					if got.Val.String != "***PASSWORD***" && got.Value != "***PASSWORD***" {
						t.Fatal("required core lost the masked field")
					}
				}
			}
			if entry.IndexedStore != nil {
				entry.IndexedStore.Iterate(func(_, value string) {
					seen++
					if value != "***PASSWORD***" {
						t.Error("required core retains a clear indexed value")
					}
				})
			}
			if seen != 1 {
				t.Fatalf("expected exactly one retained field, got %d", seen)
			}
			line := jsonfmt.NewJsonFormatter().Format(entry, nil)
			if !json.Valid(line) || bytes.Contains(line, []byte("synthetic-secret")) {
				t.Fatal("required core emitted invalid JSON or a clear secret")
			}
		})
	}
}

func TestCoreDependency_RegexPolicy(t *testing.T) {
	m := masking.NewPIIMasker()
	const input = "classified"
	_ = m.MaskString(input) // warm the previous policy, if the dependency caches it
	if err := m.AddRule(input, "[MASKED]", "regex"); err != nil {
		t.Fatal(err)
	}
	if got := m.MaskString(input); got != "[MASKED]" {
		t.Fatal("required core bypasses a configured regex or retains a stale policy")
	}
	if err := m.RemoveRule(input); err != nil {
		t.Fatal(err)
	}
	if got := m.MaskString(input); got != input {
		t.Fatal("required core retains a removed regex")
	}
	if err := m.AddRule("z", "X", "regex"); err != nil {
		t.Fatal(err)
	}
	if got := m.MaskString(strings.Repeat("z ", 100)); got != strings.Repeat("X ", 100) {
		t.Fatal("required core leaves matches beyond a fixed-size match buffer")
	}
}

func TestCoreDependency_BindMasking(t *testing.T) {
	var buf bytes.Buffer
	m := masking.NewPIIMasker()
	for _, name := range []string{"trace_id", "span_id"} {
		if err := m.AddRule(name, "[REDACTED]", "field"); err != nil {
			t.Fatal(err)
		}
	}
	logger := core.NewLogger(core.Config{
		Level: types.InfoLevel, Masker: m, EnableMasking: true,
		Adapters: []types.Adapter{console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter())},
	})
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	Bind(ctx, logger).Info("masked correlation")
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trace_id"] != "[REDACTED]" || record["span_id"] != "[REDACTED]" {
		t.Fatal("required core does not mask request-bound correlation")
	}
	if strings.Contains(buf.String(), traceID.String()) || strings.Contains(buf.String(), spanID.String()) {
		t.Fatal("required core emitted a stale correlation field")
	}
}
