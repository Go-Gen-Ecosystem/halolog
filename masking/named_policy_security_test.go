package masking

import (
	"slices"
	"sync"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

func TestNamedPolicyLifecycle(t *testing.T) {
	add := func(t *testing.T, m types.PIIMasker, name, pattern, mask string) {
		t.Helper()
		if err := m.AddPattern(name, pattern, mask); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, m types.PIIMasker, input, want string, names ...string) {
		t.Helper()
		if got := m.MaskString(input); got != want {
			t.Fatalf("MaskString(%q) = %q, want %q", input, got, want)
		}
		gotNames := m.GetPatterns()
		slices.Sort(gotNames)
		slices.Sort(names)
		if !slices.Equal(gotNames, names) {
			t.Fatalf("names = %v, want %v", gotNames, names)
		}
	}
	for _, removed := range []string{"alpha", "beta"} {
		t.Run("equal_regex_remove_"+removed, func(t *testing.T) {
			m := &piiMasker{patternNames: make(map[string]string)}
			add(t, m, "alpha", "pin", "[A]")
			add(t, m, "beta", "pin", "[B]")
			m.RemovePattern(removed)
			if removed == "alpha" {
				check(t, m, "pin", "[B]", "beta")
			} else {
				check(t, m, "pin", "[A]", "alpha")
			}
		})
	}
	t.Run("unnamed_and_empty_name", func(t *testing.T) {
		m := &piiMasker{patternNames: make(map[string]string)}
		if err := m.AddRule("pin", "[BASE]", "regex"); err != nil {
			t.Fatal(err)
		}
		add(t, m, "", "pin", "[EMPTY]")
		check(t, m, "pin", "[EMPTY]", "")
		m.RemovePattern("missing")
		check(t, m, "pin", "[EMPTY]", "")
		m.RemovePattern("")
		check(t, m, "pin", "[BASE]")
	})
	t.Run("builtin_survives_override_removal", func(t *testing.T) {
		m := NewPIIMasker()
		add(t, m, "email_override", `\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`, "[CUSTOM]")
		check(t, m, "user@example.com", "[CUSTOM]", "email_override")
		m.RemovePattern("email_override")
		check(t, m, "user@example.com", "[EMAIL]")
	})
	t.Run("replacement_and_invalid_update", func(t *testing.T) {
		m := &piiMasker{patternNames: make(map[string]string)}
		add(t, m, "policy", "pin", "[OLD]")
		add(t, m, "policy", "token", "[NEW]")
		check(t, m, "pin token", "pin [NEW]", "policy")
		if err := m.AddPattern("policy", "[", "bad"); err == nil {
			t.Fatal("invalid regex accepted")
		}
		check(t, m, "pin token", "pin [NEW]", "policy")
		add(t, m, "policy", "token", "[UPDATED]")
		check(t, m, "pin token", "pin [UPDATED]", "policy")
		if len(m.storedRules) != 1 {
			t.Fatalf("updates accumulated %d registrations", len(m.storedRules))
		}
		m.RemovePattern("policy")
		check(t, m, "pin token", "pin token")
	})
	t.Run("clone_ownership", func(t *testing.T) {
		m := &piiMasker{patternNames: make(map[string]string)}
		add(t, m, "alpha", "pin", "[A]")
		add(t, m, "beta", "pin", "[B]")
		clone := m.Clone()
		clone.RemovePattern("beta")
		check(t, clone, "pin", "[A]", "alpha")
		check(t, m, "pin", "[B]", "alpha", "beta")
		m.RemovePattern("alpha")
		check(t, clone, "pin", "[A]", "alpha")
	})
	t.Run("broad_remove_cleans_names", func(t *testing.T) {
		m := &piiMasker{patternNames: make(map[string]string)}
		add(t, m, "alpha", "pin", "[A]")
		add(t, m, "beta", "pin", "[B]")
		add(t, m, "gamma", "token", "[C]")
		if err := m.RemoveRule("pin"); err != nil {
			t.Fatal(err)
		}
		check(t, m, "pin token", "pin [C]", "gamma")
	})
}

// A live baseline must remain active while an overlapping override is replaced
// or removed. Concurrent readers may observe either complete policy, never none.
func TestNamedPolicyConcurrentRemoval(t *testing.T) {
	m := &piiMasker{patternNames: make(map[string]string)}
	if err := m.AddPattern("stable", "pin", "[BASE]"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 2000 {
				if got := m.MaskString("pin"); got != "[BASE]" && got != "[TEMP]" {
					t.Errorf("exposed value: %q", got)
					return
				}
			}
		}()
	}
	close(start)
	for range 256 {
		if err := m.AddPattern("transient", "pin", "[TEMP]"); err != nil {
			t.Error(err)
			break
		}
		m.RemovePattern("transient")
	}
	wg.Wait()
	if got := m.MaskString("pin"); got != "[BASE]" {
		t.Fatalf("stable policy lost: %q", got)
	}
}
