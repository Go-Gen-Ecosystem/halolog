package masking

import (
	"fmt"
	"runtime"
	"testing"
)

// This regression checks retained heap, not peak working memory or an RSS cap.
// Full GCs separate transient regexp/output allocations from retained caches.
// A generous noise allowance avoids treating runtime bookkeeping as a leak.
func TestMaskingUniqueInputRetention(t *testing.T) {
	m := NewPIIMasker()
	process := func(start, count int) {
		for i := start; i < start+count; i++ {
			input := fmt.Sprintf("user%08d@example.com", i)
			if got := m.MaskString(input); got != "[EMAIL]" {
				t.Fatalf("unexpected redaction: %q", got)
			}
		}
	}
	process(0, 1000) // initialize regexp/runtime pools before the baseline
	snapshot := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	before := snapshot()
	process(1000, 25000)
	middle := snapshot()
	process(26000, 100000)
	after := snapshot()
	runtime.KeepAlive(m)
	const allowance = 4 << 20
	if after > before+allowance {
		t.Fatalf("retained heap grew beyond allowance: before=%d after=%d allowance=%d", before, after, allowance)
	}
	t.Logf("unique inputs=126000 retained_heap_bytes baseline=%d after_25k=%d after_125k=%d allowance=%d", before, middle, after, allowance)
}
