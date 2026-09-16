package masking

import (
	"strconv"
	"testing"
)

func TestMaskingRegexOwnedResultsSurviveReuse(t *testing.T) {
	m := NewPIIMasker()
	// Distinct prefixes remain in the result. Retaining them prevents a pooled
	// or reused mutable buffer from masquerading as an allocation optimization.
	results := make([]string, 1024)
	want := make([]string, len(results))
	for i := range results {
		prefix := strconv.Itoa(i) + ": "
		results[i] = m.MaskString(prefix + "user@example.com !")
		want[i] = prefix + "[EMAIL] !"
	}
	for range 3 {
		for i := range results {
			_ = m.MaskString("unrelated" + strconv.Itoa(i) + "@example.com")
		}
	}
	for i := range results {
		if results[i] != want[i] {
			t.Fatalf("retained result %d changed: got %q, want %q", i, results[i], want[i])
		}
	}
}
