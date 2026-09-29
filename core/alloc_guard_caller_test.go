//go:build !race

package core

// Allocation contracts run without race instrumentation: under -race,
// sync.Pool drops items on purpose, so allocation counts stop meaning anything.
import (
	"io"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/types"
)

// TestZeroAlloc_Caller guards call-site capture: once a call site has logged,
// its later lines allocate nothing, on both encode paths, through a bound
// child, and at every non-terminal level; a filtered line stays free too.
func TestZeroAlloc_Caller(t *testing.T) {
	direct := New().Level(types.TraceLevel).Caller().
		Adapter(console.NewWithWriter(io.Discard, jsonfmt.NewJsonFormatter())).MustBuild()
	capture := New().Level(types.TraceLevel).Caller().Masking(redactMasker{}).
		Adapter(console.NewWithWriter(io.Discard, jsonfmt.NewJsonFormatter())).MustBuild()
	loggers := map[string]*Logger{
		"direct":        direct,
		"capture":       capture,
		"bound-direct":  direct.With().WithString("request_id", "req-42").Logger(),
		"bound-capture": capture.With().WithString("request_id", "req-42").Logger(),
	}
	for name, l := range loggers {
		lines := map[string]func(){
			"trace":   func() { l.Trace("hot path") },
			"debug":   func() { l.Debug("hot path") },
			"info":    func() { l.Info("hot path") },
			"warn":    func() { l.Warn("hot path") },
			"error":   func() { l.Error("hot path") },
			"line":    func() { l.InfoLine().WithInt("status", 200).Msg("hot path") },
			"typed":   func() { l.Typed().WithString("k", "v").Error("hot path") },
			"classic": func() { l.WithField("k", "v").Warn("hot path") },
		}
		for kind, line := range lines {
			line() // first line from this site resolves it
			if allocs := testing.AllocsPerRun(1000, line); allocs != 0 {
				t.Errorf("%s %s: line with caller must allocate 0 times/op, got %.2f", name, kind, allocs)
			}
		}
	}
	filtered := New().Level(types.WarnLevel).Caller().
		Adapter(console.NewWithWriter(io.Discard, jsonfmt.NewJsonFormatter())).MustBuild()
	if allocs := testing.AllocsPerRun(1000, func() { filtered.Info("filtered") }); allocs != 0 {
		t.Errorf("filtered line: %.2f allocs/op", allocs)
	}
}
