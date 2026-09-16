//go:build !race

package masking

import (
	"strconv"
	"strings"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

var regexMaskSink string
var regexFieldSink types.TypedFieldData

// These budgets cover warmed public calls, not configuration or regexp's first
// machine allocation. Inputs are restored on every call, never already masked.
func TestMaskingRegexAllocationBudgets(t *testing.T) {
	m := NewPIIMasker()
	inputs := make([]string, 4096)
	for i := range inputs {
		inputs[i] = "user" + strconv.Itoa(i) + "@example.com"
		if got := m.MaskString(inputs[i]); got != "[EMAIL]" {
			t.Fatalf("corpus %d: %q", i, got)
		}
	}
	for _, tc := range []struct {
		name, input, want string
		allocs            float64
	}{
		{"no_match", "safe message", "safe message", 0},
		{"complete_redaction", "user@example.com", "[EMAIL]", 0},
		{"owned_partial_redaction", ". user@example.com !", ". [EMAIL] !", 1},
		{"owned_long_redaction", strings.Repeat(". ", 110) + "user@example.com", strings.Repeat(". ", 110) + "[EMAIL]", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.MaskString(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			got := testing.AllocsPerRun(1000, func() { regexMaskSink = m.MaskString(tc.input) })
			if got != tc.allocs {
				t.Fatalf("allocs=%v, want %v", got, tc.allocs)
			}
		})
	}
	t.Run("4096_distinct_whole_values", func(t *testing.T) {
		i := 0
		got := testing.AllocsPerRun(8192, func() {
			regexMaskSink = m.MaskString(inputs[i&4095])
			i++
		})
		if got != 0 {
			t.Fatalf("allocs=%v, want 0", got)
		}
	})
	for _, storage := range []string{"typed", "legacy"} {
		t.Run("constant_field_"+storage, func(t *testing.T) {
			original := types.TypedFieldData{Key: "note", Val: types.StringValue("user@example.com")}
			if storage == "legacy" {
				original.Value = "user@example.com"
			}
			var field types.TypedFieldData
			got := testing.AllocsPerRun(1000, func() {
				field = original
				m.MaskField(&field)
				regexFieldSink = field
			})
			if got != 0 {
				t.Fatalf("allocs=%v, want 0", got)
			}
			if field.Val.String != "[EMAIL]" || field.Value != "[EMAIL]" {
				t.Fatalf("both field representations must be redacted: %#v", field)
			}
		})
	}
}

// A configured replacement can expand its match. Reserving the final size
// should produce the complete owned result without repeated buffer growth.
func TestMaskingRegexExpandedOutputAllocation(t *testing.T) {
	replacement := strings.Repeat("MASK", 128)
	rule, err := newRegexRule("a", replacement)
	if err != nil {
		t.Fatal(err)
	}
	pm := &piiMasker{}
	snap := &maskerSnapshot{regexRules: []regexRule{rule}}
	const input = "!!a!!"
	want := "!!" + replacement + "!!"
	var result string
	allocs := testing.AllocsPerRun(200, func() {
		result = pm.maskString(input, snap)
	})
	if result != want {
		t.Fatalf("expanded output = %q, want %q", result, want)
	}
	if allocs > 1 {
		t.Fatalf("expanded partial output allocated %v times; want at most one owned buffer", allocs)
	}
}
