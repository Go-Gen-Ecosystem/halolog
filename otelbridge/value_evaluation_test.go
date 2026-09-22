package otelbridge

import (
	"sync/atomic"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

type countingValueError struct{ calls atomic.Int64 }

func (e *countingValueError) Error() string {
	e.calls.Add(1)
	return "counted error"
}

func TestAdapter_UnrelatedValuesAreEvaluatedAtMostOnce(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			value := &countingValueError{}
			provider := &recorder{disableAll: !enabled}
			entry := &types.LogEntry{Level: types.InfoLevel, Message: "value", Fields: []types.TypedFieldData{
				{Key: "payload_error", Value: value},
			}}
			if err := NewAdapter("test/value-count", WithLoggerProvider(provider)).Write(entry); err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if enabled {
				want = 1
			}
			if got := value.calls.Load(); got != want {
				t.Fatalf("Error calls = %d, want %d", got, want)
			}
		})
	}
}

func TestAdapter_ShadowedMetadataErrorIsNotEvaluated(t *testing.T) {
	value := &countingValueError{}
	entry := &types.LogEntry{
		Level: types.InfoLevel, Message: "shadow", Error: value,
		Fields: []types.TypedFieldData{{Key: attrError, Val: types.StringValue("explicit")}},
	}
	if err := benchAdapter().Write(entry); err != nil {
		t.Fatal(err)
	}
	if got := value.calls.Load(); got != 0 {
		t.Fatalf("shadowed metadata Error calls = %d, want 0", got)
	}
}
