package callsite

import (
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

//go:noinline
func here() uintptr {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return pcs[0]
}

func TestLookupResolvesOnceAndShares(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	pc := here()
	first := Lookup(pc)
	if filepath.Base(first.File) != "callsite_test.go" || first.Line != line-5 {
		t.Fatalf("resolved %s:%d, want callsite_test.go:%d", first.File, first.Line, line-5)
	}
	if Lookup(pc) != first {
		t.Fatal("a second lookup must return the cached site")
	}
	if first.File != file {
		t.Fatalf("resolved file %q, want %q", first.File, file)
	}
}

//go:noinline
func callerOf(skip int) *Site { return Caller(skip) }

func TestCallerSkip(t *testing.T) {
	_, file, marker, _ := runtime.Caller(0)
	if site := callerOf(0); site.File != file || site.Line != marker+1 {
		t.Fatalf("Caller(0) = %s:%d, want %s:%d", site.File, site.Line, file, marker+1)
	}
	for _, skip := range []int{-1, MaxSkip + 1} {
		if site := callerOf(skip); site != unknown {
			t.Fatalf("Caller(%d) = %+v, want the zero site", skip, site)
		}
	}
}

//go:noinline
func memberSite() uintptr {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return pcs[0]
}

func TestZeroSiteAndMember(t *testing.T) {
	renders := 0
	render := func(dst []byte, file string, line int) []byte {
		renders++
		return append(dst, filepath.Base(file)...)
	}
	if Lookup(0) != unknown || unknown.Member(render) != nil {
		t.Fatal("the zero site must render nothing")
	}
	pc := memberSite()
	sites.Delete(pc) // a fresh site on every run, so -count repeats stay independent
	site := Lookup(pc)
	a, b := site.Member(render), site.Member(render)
	if string(a) != "callsite_test.go" || &a[0] != &b[0] || renders != 1 {
		t.Fatalf("member %q/%q rendered %d times, want one shared rendering", a, b, renders)
	}
}

func TestRetentionCeiling(t *testing.T) {
	saved := retained.Load()
	defer retained.Store(saved)
	pc := here() + 1 // a PC no other test resolves
	sites.Delete(pc)
	retained.Store(maxSites)
	if site := Lookup(pc); site.Line == 0 {
		t.Fatal("over the ceiling a site must still resolve")
	}
	if _, kept := sites.Load(pc); kept || retained.Load() != maxSites {
		t.Fatalf("ceiling not honoured: kept=%v retained=%d", kept, retained.Load())
	}
}

func TestConcurrentFirstSighting(t *testing.T) {
	pc := here() + 2
	sites.Delete(pc)
	before := Retained()
	got := make([]*Site, 16)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = Lookup(pc)
		}(i)
	}
	wg.Wait()
	for _, s := range got {
		if s != got[0] {
			t.Fatal("racing first sightings must converge on one site")
		}
	}
	if Retained() != before+1 {
		t.Fatalf("retained %d -> %d, want one new site", before, Retained())
	}
}

//go:noinline
func slotSiteA() uintptr {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return pcs[0]
}

//go:noinline
func slotSiteB() uintptr {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return pcs[0]
}

// Two sites race for the last free slot: exactly one is retained, both
// resolve correctly, and each shares one published rendering.
func TestLastSlotUnderConcurrency(t *testing.T) {
	pcs := []uintptr{slotSiteA(), slotSiteB()}
	saved := retained.Load()
	for _, pc := range pcs {
		sites.Delete(pc)
	}
	retained.Store(maxSites - 1)
	defer func() {
		for _, pc := range pcs {
			sites.Delete(pc)
		}
		retained.Store(saved)
	}()
	var renders atomic.Int64
	render := func(dst []byte, file string, line int) []byte {
		renders.Add(1)
		return append(dst, filepath.Base(file)...)
	}
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				s := Lookup(pcs[i%2])
				if filepath.Base(s.File) != "callsite_test.go" || s.Line <= 0 || string(s.Member(render)) != "callsite_test.go" {
					t.Errorf("invalid site %+v", s)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	kept := 0
	for _, pc := range pcs {
		if _, ok := sites.Load(pc); ok {
			kept++
		}
	}
	if kept != 1 || retained.Load() != maxSites {
		t.Fatalf("kept %d sites with counter %d, want 1 and %d", kept, retained.Load(), maxSites)
	}
}
