package masking

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/types"
)

func TestMaskingSecurityRepresentations(t *testing.T) {
	for _, storage := range []string{"typed", "boxed", "descriptor", "static", "context", "static_context", "indexed"} {
		t.Run(storage, func(t *testing.T) {
			m := NewPIIMasker()
			f := types.TypedFieldData{Key: "password", Val: types.StringValue("hunter2")}
			e := &types.LogEntry{Message: "test", Level: types.InfoLevel}
			switch storage {
			case "typed":
				e.Fields = []types.TypedFieldData{f}
			case "boxed":
				f.Val = types.FieldValue{}
				f.Value = "hunter2"
				e.Fields = []types.TypedFieldData{f}
			case "descriptor":
				f.Key = ""
				f.KeyDesc = &types.FieldKey{Name: "password", JSONFragment: []byte(`,"password":`)}
				e.Fields = []types.TypedFieldData{f}
			case "static":
				e.StaticFields = []types.TypedFieldData{f}
				e.StaticFieldCount = 1
			case "context":
				e.Context = []types.TypedFieldData{f}
			case "static_context":
				e.StaticContext = []types.TypedFieldData{f}
				e.StaticContextCount = 1
			case "indexed":
				e.EnableIndexedStorage(8)
				e.AddIndexedField("password", "hunter2")
			}
			m.Apply(e)
			for _, fields := range [][]types.TypedFieldData{e.Fields, e.StaticFields, e.Context, e.StaticContext} {
				for _, got := range fields {
					if got.Val.String == "hunter2" || got.Value == "hunter2" {
						t.Fatal("secret retained in a field representation")
					}
					if got.Val.String != "***PASSWORD***" && got.Value != "***PASSWORD***" {
						t.Fatal("redaction missing")
					}
				}
			}
			if e.IndexedStore != nil {
				e.IndexedStore.Iterate(func(k, v string) {
					if v != "***PASSWORD***" {
						t.Errorf("indexed secret: %q", v)
					}
				})
			}
			if line := jsonfmt.NewJsonFormatter().Format(e, nil); bytes.Contains(line, []byte("hunter2")) {
				t.Fatalf("JSON leak: %s", line)
			}
		})
	}
}

func TestMaskingSecurityInvalidRuleIsAtomic(t *testing.T) {
	m := NewPIIMasker()
	if err := m.AddRule("[", "ignored", "regex"); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if err := m.AddRule("password", "ignored", "typo"); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if err := m.AddPattern("invalid", "[", "ignored"); err == nil {
		t.Fatal("invalid named regex accepted")
	}
	if len(m.GetPatterns()) != 0 {
		t.Fatal("invalid pattern changed configuration")
	}
	f := types.TypedFieldData{Key: "password", Val: types.StringValue("hunter2")}
	m.MaskField(&f)
	if f.Val.String != "***PASSWORD***" {
		t.Fatal("failed update changed active policy")
	}
}

func TestMaskingSecurityOverlapAndOwnedOutput(t *testing.T) {
	m := NewPIIMasker().(*piiMasker)
	m.storedRules = nil
	if err := m.AddRule("abc", "X", "regex"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRule("bcdef", "Y", "regex"); err != nil {
		t.Fatal(err)
	}
	if got := m.MaskString("abcdef!"); got != "X!" {
		t.Fatalf("overlap leaked a suffix: %q", got)
	}
	retained := m.MaskString("abc!")
	for i := 0; i < 100; i++ {
		_ = m.MaskString(strings.Repeat("abc", i))
	}
	if retained != "X!" {
		t.Fatalf("retained output changed: %q", retained)
	}
}

func TestMaskingSecurityConcurrentPolicyUpdates(t *testing.T) {
	m := NewPIIMasker()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				e := &types.LogEntry{Fields: []types.TypedFieldData{{Key: "password", Val: types.StringValue("hunter2")}}}
				m.Apply(e)
				if e.Fields[0].Val.String != "***PASSWORD***" {
					t.Error("fixed field rule lost during regex updates")
					return
				}
				_ = m.MaskString("classified")
			}
		}()
	}
	for range 25 {
		if err := m.AddRule("classified", "X", "regex"); err != nil {
			t.Fatal(err)
		}
		if err := m.RemoveRule("classified"); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

// A single regex has exactly the standard library's literal replacement
// semantics, including zero-width matches, invalid UTF-8 and replacement '$'.
func FuzzMaskingLiteralOracle(f *testing.F) {
	f.Add("z z z", "z", "X")
	f.Add("αβ", "", "$1")
	f.Add(strings.Repeat("z", 65), "z", "[MASK]")
	f.Fuzz(func(t *testing.T, input, pattern, replacement string) {
		if len(input) > 4096 || len(pattern) > 128 || len(replacement) > 128 {
			t.Skip()
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Skip()
		}
		m := &piiMasker{}
		rule, err := newRegexRule(pattern, replacement)
		if err != nil {
			t.Fatal(err)
		}
		snap := &maskerSnapshot{regexRules: []regexRule{rule}}
		want := re.ReplaceAllLiteralString(input, replacement)
		if got := m.maskString(input, snap); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}

func TestMaskingSecurityRegex(t *testing.T) {
	m := NewPIIMasker()
	_ = m.MaskString("classified") // warm old policy
	if err := m.AddRule("classified", "[MASKED]", "regex"); err != nil {
		t.Fatal(err)
	}
	e := &types.LogEntry{Message: "classified", Fields: []types.TypedFieldData{{Key: "note", Val: types.StringValue("classified")}}}
	m.Apply(e)
	if e.Message != "[MASKED]" || e.Fields[0].Val.String != "[MASKED]" {
		t.Fatalf("custom regex bypassed: %+v", e)
	}
	if got := m.MaskString("classified"); got != "[MASKED]" {
		t.Fatalf("stale policy: %q", got)
	}
	if err := m.RemoveRule("classified"); err != nil {
		t.Fatal(err)
	}
	if got := m.MaskString("classified"); got != "classified" {
		t.Fatalf("removed rule still active: %q", got)
	}
	if err := m.AddRule("z", "X", "regex"); err != nil {
		t.Fatal(err)
	}
	if got := m.MaskString(strings.Repeat("z ", 100)); strings.Contains(got, "z") {
		t.Fatalf("match overflow leaked: %q", got)
	}
}

func TestMaskingSecurityFieldRuleZeroAlloc(t *testing.T) {
	m := NewPIIMasker()
	var f types.TypedFieldData
	n := testing.AllocsPerRun(1000, func() {
		f = types.TypedFieldData{Key: "password", Val: types.StringValue("hunter2")}
		m.MaskField(&f)
	})
	if n != 0 {
		t.Fatalf("field rule allocated: %v", n)
	}
	if f.Val.String != "***PASSWORD***" {
		t.Fatal("typed redaction missing")
	}
}
