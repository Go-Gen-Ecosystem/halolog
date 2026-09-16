package masking

import (
	"runtime"
	"strings"
	"testing"
)

// A tiny partial redaction must not retain an input-sized output buffer. The
// same input is reused so this measures retained result storage, not input
// diversity or a cache. Each result stays live across the final full GC.
func TestMaskingRegexPartialOutputRetention(t *testing.T) {
	rule, err := newRegexRule("a+", "X")
	if err != nil {
		t.Fatal(err)
	}
	pm := &piiMasker{}
	snap := &maskerSnapshot{regexRules: []regexRule{rule}}
	input := "!" + strings.Repeat("a", 1<<20) + "!"
	for range 2 {
		if got := pm.maskString(input, snap); got != "!X!" {
			t.Fatalf("warmup result = %q, want !X!", got)
		}
	}
	liveHeap := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	before := liveHeap()
	var results [64]string
	for i := range results {
		results[i] = pm.maskString(input, snap)
		if results[i] != "!X!" {
			t.Fatalf("result %d = %q, want !X!", i, results[i])
		}
	}
	after := liveHeap()
	// Keep all measured inputs, configuration, and outputs live at both samples.
	// In particular, the compiler must not collect the 64 retained result buffers
	// before liveHeap runs just because their contents were already checked.
	runtime.KeepAlive(results)
	runtime.KeepAlive(input)
	runtime.KeepAlive(snap)
	runtime.KeepAlive(pm)
	const allowance = 4 << 20
	if after > before+allowance {
		t.Fatalf("64 three-byte results retained excessive heap: before=%d after=%d allowance=%d", before, after, allowance)
	}
	t.Logf("retained_results=%d input_bytes=%d output_bytes=3 heap_before=%d heap_after=%d allowance=%d", len(results), len(input), before, after, allowance)
}
