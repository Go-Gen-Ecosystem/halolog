package otelbridge

import (
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

type capacityPanicError struct{}

func (capacityPanicError) Error() string { panic("capacity must not evaluate user values") }

func TestAttributeCapacityBoundsAllStorage(t *testing.T) {
	for _, width := range []int{0, 5, 6, 8, 9, 16, 17, 64} {
		for _, storage := range []string{"fields", "static", "context", "static_context", "indexed"} {
			entry := entryWith(width)
			switch storage {
			case "static":
				entry.StaticFields, entry.StaticFieldCount, entry.Fields = entry.Fields, width, nil
			case "context":
				entry.Context, entry.Fields = entry.Fields, nil
			case "static_context":
				entry.StaticContext, entry.StaticContextCount, entry.Fields = entry.Fields, width, nil
			case "indexed":
				entry.EnableIndexedStorage(8)
				for _, f := range entry.Fields {
					entry.AddIndexedField(f.Key, "value")
				}
				entry.Fields = nil
			}
			_, _, fieldCount := benchAdapter().spanContext(entry)
			if got := attributeCapacity(entry, fieldCount); got != width {
				t.Fatalf("%s/%d: capacity=%d", storage, width, got)
			}
			entry.Component, entry.ErrorMsg, entry.File, entry.Line = "component", "error", "file.go", 1
			if got := attributeCapacity(entry, fieldCount); got != width+4 {
				t.Fatalf("%s/%d: metadata capacity=%d", storage, width, got)
			}
		}
	}
}

func TestAttributeCapacityDoesNotEvaluateValues(t *testing.T) {
	entry := &types.LogEntry{Error: capacityPanicError{}, StaticFieldCount: -1, StaticContextCount: 99}
	if got := attributeCapacity(entry, 0); got != 1 {
		t.Fatalf("capacity=%d", got)
	}
}
