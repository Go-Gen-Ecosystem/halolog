//go:build !race

package otelbridge

// Allocation contracts run without race/coverage instrumentation. Ownership and
// correlation tests remain in the race build; CI runs both configurations.
import (
	"strconv"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

func TestAdapter_AllocationBudgets(t *testing.T) {
	a := benchAdapter()
	for _, tc := range allocBudget {
		t.Run(tc.name, func(t *testing.T) {
			entry := tc.entry()
			got := testing.AllocsPerRun(200, func() { _ = a.Write(entry) })
			if got > tc.budget {
				t.Fatalf("%.1f allocs/op exceeds the budget of %.0f", got, tc.budget)
			}
			t.Logf("%.1f allocs/op (budget %.0f)", got, tc.budget)
		})
	}
}

func TestAdapter_DisabledSeverityPaysOnlyForCorrelation(t *testing.T) {
	a := NewAdapter("halolog/otelbridge_bench", WithLoggerProvider(&recorder{disableAll: true}))

	wide := correlatedEntry()
	for i := range 12 {
		wide.Fields = append(wide.Fields, types.TypedFieldData{
			Key: "extra" + strconv.Itoa(i),
			Val: types.StringValue("value"),
		})
	}

	cases := []struct {
		name   string
		entry  *types.LogEntry
		budget float64
	}{
		{"uncorrelated costs nothing", entryWith(9), 0},
		{"byte payload is not copied", bytePayloadEntry(), 0},
		{"correlated pays for its span context", correlatedEntry(), 2},
		{"attributes add nothing to a dropped record", wide, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testing.AllocsPerRun(200, func() { _ = a.Write(tc.entry) })
			if got > tc.budget {
				t.Fatalf("%.1f allocs/op exceeds the budget of %.0f", got, tc.budget)
			}
			t.Logf("%.1f allocs/op (budget %.0f)", got, tc.budget)
		})
	}
}
