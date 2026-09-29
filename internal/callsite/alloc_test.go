//go:build !race

package callsite

import (
	"testing"
)

// Allocation contracts run without race instrumentation, like the other
// allocation guards in this module.
func TestZeroAlloc_WarmLookupAndMember(t *testing.T) {
	pc := here()
	render := func(dst []byte, file string, line int) []byte { return append(dst, file...) }
	Lookup(pc).Member(render)
	if allocs := testing.AllocsPerRun(1000, func() { Lookup(pc).Member(render) }); allocs != 0 {
		t.Fatalf("a warm lookup and member must allocate 0 times/op, got %.2f", allocs)
	}
}

// Past the retention ceiling a site is resolved again on every lookup: the
// documented, bounded-memory fallback, which allocates.
func TestUnretainedSiteResolvesAgain(t *testing.T) {
	pc := slotSiteA()
	sites.Delete(pc)
	saved := retained.Load()
	retained.Store(maxSites)
	defer retained.Store(saved)
	render := func(dst []byte, file string, line int) []byte { return append(dst, file...) }
	if allocs := testing.AllocsPerRun(100, func() { Lookup(pc).Member(render) }); allocs == 0 {
		t.Fatal("an unretained site must be resolved again (and allocate) on each lookup")
	}
}
