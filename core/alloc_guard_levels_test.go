//go:build !race

package core

// Allocation contracts run without race instrumentation: under -race,
// sync.Pool drops items on purpose, so allocation counts stop meaning anything.
// Correctness of these paths stays covered by the race build.
import (
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

// TestZeroAlloc_MessageOnlyEveryLevel guards message-only lines at every
// non-terminal level on each dispatch shape the logger specialises: one or
// several plain adapters, masking, sampling, and a raw-capable adapter whose
// formatter offers no direct JSON encoder. The guards above cover Info and
// the direct path; the other levels and shapes used to build their entry on
// the stack and hand it to an interface, which moved it to the heap per line.
func TestZeroAlloc_MessageOnlyEveryLevel(t *testing.T) {
	plain := func(n int) []types.Adapter {
		adapters := make([]types.Adapter, n)
		for i := range adapters {
			adapters[i] = &benchmarkAdapter{}
		}
		return adapters
	}
	shapes := []struct {
		name string
		cfg  Config
	}{
		{"one-adapter", Config{Adapters: plain(1)}},
		{"two-adapters", Config{Adapters: plain(2)}},
		{"masked-one", Config{Adapters: plain(1), EnableMasking: true, Masker: redactMasker{}}},
		{"masked-two", Config{Adapters: plain(2), EnableMasking: true, Masker: redactMasker{}}},
		{"sampled", Config{Adapters: plain(1), EnableSampling: true, Sampler: &fixedSampler{sample: true}}},
		{"raw-without-json-encoder", Config{Adapters: []types.Adapter{&rawNull{}}}},
	}
	for _, shape := range shapes {
		shape.cfg.Component = "guard"
		shape.cfg.Level = types.TraceLevel
		l := NewLogger(shape.cfg)
		levels := []struct {
			name string
			log  func(string)
		}{{"trace", l.Trace}, {"debug", l.Debug}, {"info", l.Info}, {"warn", l.Warn}, {"error", l.Error}}
		for _, level := range levels {
			if allocs := testing.AllocsPerRun(1000, func() { level.log("hot path message") }); allocs != 0 {
				t.Errorf("%s %s: message-only line must allocate 0 times/op, got %.2f", shape.name, level.name, allocs)
			}
		}
	}
}
