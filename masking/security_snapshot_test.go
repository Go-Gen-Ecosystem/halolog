package masking

import (
	"sync"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
)

// Removing one named policy must not silently remove another active policy
// that happens to use the same regex. GetPatterns must describe live policy.
func TestSecurityNamedPoliciesRemainIndependent(t *testing.T) {
	m := NewPIIMasker()
	if err := m.AddPattern("alpha", "pin", "[A]"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddPattern("beta", "pin", "[B]"); err != nil {
		t.Fatal(err)
	}
	m.RemovePattern("alpha")
	if got := m.MaskString("pin"); got != "[B]" {
		t.Fatalf("removing alpha disabled beta: got %q, want [B]; named policies=%v", got, m.GetPatterns())
	}
}

func TestSecurityApplyUsesOnePolicySnapshot(t *testing.T) {
	m := &piiMasker{}
	if err := m.AddRule("secret", "A", "field"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 256 {
				field := types.TypedFieldData{Key: "secret", Val: types.StringValue("synthetic")}
				entry := &types.LogEntry{
					Fields: []types.TypedFieldData{field}, Context: []types.TypedFieldData{field},
					StaticFields: []types.TypedFieldData{field}, StaticFieldCount: 1,
					StaticContext: []types.TypedFieldData{field}, StaticContextCount: 1,
				}
				m.Apply(entry)
				want := entry.Fields[0].Val.String
				if want != "A" && want != "B" {
					t.Error("unmasked value")
					return
				}
				for _, fields := range [][]types.TypedFieldData{entry.Fields, entry.Context, entry.StaticFields, entry.StaticContext} {
					if fields[0].Val.String != want || fields[0].Value != want {
						t.Error("mixed policy or stale representation in one entry")
						return
					}
				}
			}
		}()
	}
	for i := range 64 {
		if err := m.AddRule("secret", []string{"A", "B"}[i%2], "field"); err != nil {
			t.Error(err)
			break
		}
	}
	wg.Wait()
}
