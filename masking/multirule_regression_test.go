package masking

import (
	"regexp"
	"strings"
	"testing"
)

type multiRuleSpec struct {
	pattern, replacement string
}

type multiRuleCompiled struct {
	re          *regexp.Regexp
	replacement string
}

// multiRuleOracle derives components from byte coverage and strict interior
// boundary coverage. It intentionally does not sort or merge match intervals,
// call maskString, or read production snapshots. Adjacent intervals have no
// shared interior boundary, so they produce separate replacements.
//
// Zero-width matches insert text at an uncovered boundary. At a component's
// first boundary, insertions ordered before its first consuming rule survive;
// subsequent insertions at that boundary or in its interior are absorbed.
func multiRuleOracle(input string, rules []multiRuleCompiled) string {
	type delta struct{ bytes, joins int32 }
	type event struct {
		consumes    bool
		replacement string
	}
	deltas := make([]delta, len(input)+2)
	events := make(map[int][]event)
	for _, rule := range rules {
		for _, match := range rule.re.FindAllStringIndex(input, -1) {
			start, end := match[0], match[1]
			events[start] = append(events[start], event{end > start, rule.replacement})
			if end > start {
				deltas[start].bytes++
				deltas[end].bytes--
			}
			if end > start+1 {
				deltas[start+1].joins++
				deltas[end].joins--
			}
		}
	}
	var out strings.Builder
	out.Grow(len(input))
	var covered, joined int32
	for pos := 0; pos <= len(input); pos++ {
		covered += deltas[pos].bytes
		joined += deltas[pos].joins
		if joined == 0 {
			for _, match := range events[pos] {
				out.WriteString(match.replacement)
				if match.consumes {
					break
				}
			}
		}
		if pos < len(input) && covered == 0 {
			out.WriteByte(input[pos])
		}
	}
	return out.String()
}

func compileMultiRules(t testing.TB, specs []multiRuleSpec) []multiRuleCompiled {
	t.Helper()
	rules := make([]multiRuleCompiled, len(specs))
	for i, spec := range specs {
		re, err := regexp.Compile(spec.pattern)
		if err != nil {
			t.Fatalf("compile rule %d: %v", i, err)
		}
		rules[i] = multiRuleCompiled{re, spec.replacement}
	}
	return rules
}

func newMultiRuleMasker(t testing.TB, specs []multiRuleSpec) *piiMasker {
	t.Helper()
	m := &piiMasker{patternNames: make(map[string]string)}
	for _, spec := range specs {
		if err := m.AddRule(spec.pattern, spec.replacement, "regex"); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestMaskingMultiRuleGolden(t *testing.T) {
	tests := []struct {
		name, input, want string
		rules             []multiRuleSpec
	}{
		{"no_rules", "abc\xff", "abc\xff", nil},
		{"no_matches", "abc\xff", "abc\xff", []multiRuleSpec{{"z", "X"}, {"q", "Y"}}},
		{"earlier_start_beats_rule_order", "abcdef!", "X!", []multiRuleSpec{{"cdef", "Y"}, {"abc", "X"}}},
		{"same_start_short_first", "abcdef!", "X!", []multiRuleSpec{{"abc", "X"}, {"abcdef", "Y"}}},
		{"same_start_long_first", "abcdef!", "Y!", []multiRuleSpec{{"abcdef", "Y"}, {"abc", "X"}}},
		{"transitive_overlap", "abcdefg!", "X!", []multiRuleSpec{{"efg", "Z"}, {"abc", "X"}, {"cde", "Y"}}},
		{"nested", "abcdef!", "X!", []multiRuleSpec{{"bcde", "Y"}, {"abcdef", "X"}, {"cd", "Z"}}},
		{"adjacent", "abcd!", "XY!", []multiRuleSpec{{"cd", "Y"}, {"ab", "X"}}},
		{"overlap_then_adjacent", "abcdef!", "XZ!", []multiRuleSpec{{"abc", "X"}, {"bcde", "Y"}, {"f", "Z"}}},
		{"literal_replacements", "ab cd", "$1${name}$$\\0 ", []multiRuleSpec{{"(ab)", "$1${name}$$\\0"}, {"cd", ""}}},
		{"original_input_only", "abc sensitive", "sensitive HIDDEN", []multiRuleSpec{{"abc", "sensitive"}, {"sensitive", "HIDDEN"}}},
		{"zero_before_consuming_tie", "ab!", "ZX!", []multiRuleSpec{{"^", "Z"}, {"ab", "X"}}},
		{"zero_after_consuming_tie", "ab!", "X!", []multiRuleSpec{{"ab", "X"}, {"^", "Z"}}},
		{"zero_at_end", "ab", "XZ", []multiRuleSpec{{"$", "Z"}, {"ab", "X"}}},
		{"zero_interior_absorbed", "abc", "X", []multiRuleSpec{{`\B`, "Z"}, {"abc", "X"}}},
		{"multiple_zero_ties", "ab", "12X2", []multiRuleSpec{{"^", "1"}, {`\b`, "2"}, {"ab", "X"}}},
		{"empty_input_zero_ties", "", "AB", []multiRuleSpec{{"^", "A"}, {"$", "B"}}},
		{"unicode_zero_boundaries", "αβ", "|A|β|", []multiRuleSpec{{"", "|"}, {"α", "A"}}},
		{"unicode_consuming_tie", "αβ", "A|β|", []multiRuleSpec{{"α", "A"}, {"", "|"}}},
		{"invalid_utf8_preserved", "\xffabc\xc0|\xfe", "\xffX\xc0|\xfe", []multiRuleSpec{{"ab", "X"}, {"bc", "Y"}}},
		{"invalid_utf8_matched", "\xffabc\xc0|\xfe", "?X?|?", []multiRuleSpec{{"\uFFFD", "?"}, {"abc", "X"}}},
		{"invalid_utf8_zero_boundaries", "\xffa", "|?||", []multiRuleSpec{{"", "|"}, {"\uFFFD", "?"}, {"a", ""}}},
		{"match_storage_overflow", strings.Repeat("abcdef!", 65), strings.Repeat("X!", 65), []multiRuleSpec{{"abc", "X"}, {"cde", "Y"}, {"ef", "Z"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if oracle := multiRuleOracle(tc.input, compileMultiRules(t, tc.rules)); oracle != tc.want {
				t.Fatalf("oracle got %q; independently specified golden is %q", oracle, tc.want)
			}
			if got := newMultiRuleMasker(t, tc.rules).MaskString(tc.input); got != tc.want {
				t.Fatalf("masker got %q; want %q", got, tc.want)
			}
		})
	}
}

func TestMaskingMultiRuleLongInputs(t *testing.T) {
	t.Run("64KiB_dense_overlaps", func(t *testing.T) {
		const size = 64 << 10
		rules := []multiRuleSpec{{"abc", "$1"}, {"cde", "Y"}, {"ef", "Z"}}
		input := strings.Repeat("abcdef!", size/7) + strings.Repeat("!", size%7)
		want := strings.Repeat("$1!", size/7) + strings.Repeat("!", size%7)
		checkMultiRuleLargeResult(t, input, want, rules)
	})
	t.Run("1MiB_sparse_boundary_crossing", func(t *testing.T) {
		const size = 1 << 20
		const secret = "SECRETtailEND"
		var input, want strings.Builder
		end := 0
		for _, start := range []int{4090, 65530, size - len(secret)} {
			padding := strings.Repeat(".", start-end)
			input.WriteString(padding)
			want.WriteString(padding)
			input.WriteString(secret)
			want.WriteString("[MASK]")
			end = start + len(secret)
		}
		checkMultiRuleLargeResult(t, input.String(), want.String(), []multiRuleSpec{{"SECRETtail", "[MASK]"}, {"tailEND", "SUFFIX"}})
	})
	t.Run("1MiB_whole_input_overlap", func(t *testing.T) {
		const size = 1 << 20
		left := strings.Repeat(".", (size-11)/2)
		right := strings.Repeat(".", size-11-len(left))
		input := "BEGIN" + left + "MID" + right + "END"
		checkMultiRuleLargeResult(t, input, "[ALL]", []multiRuleSpec{{"BEGIN.*MID", "[ALL]"}, {"MID.*END", "[END]"}})
	})
	t.Run("1MiB_no_match_with_invalid_utf8", func(t *testing.T) {
		input := strings.Repeat(".\xff", 1<<19)
		checkMultiRuleLargeResult(t, input, input, []multiRuleSpec{{"secret", "X"}, {"token", "Y"}})
	})
}

func checkMultiRuleLargeResult(t *testing.T, input, want string, rules []multiRuleSpec) {
	t.Helper()
	if oracle := multiRuleOracle(input, compileMultiRules(t, rules)); oracle != want {
		t.Fatalf("oracle mismatch for %d-byte input: got %d bytes, want %d", len(input), len(oracle), len(want))
	}
	if got := newMultiRuleMasker(t, rules).MaskString(input); got != want {
		t.Fatalf("masker mismatch for %d-byte input: got %d bytes, want %d", len(input), len(got), len(want))
	}
}

func TestMaskingMultiRuleConfigurationUpdates(t *testing.T) {
	input := "abcdef abc!"
	specs := []multiRuleSpec{{"abc", "FIRST"}, {"bcdef", "SECOND"}}
	m := newMultiRuleMasker(t, specs)
	check := func(label string, candidate *piiMasker, policy []multiRuleSpec) {
		t.Helper()
		want := multiRuleOracle(input, compileMultiRules(t, policy))
		for i := 0; i < 3; i++ {
			if got := candidate.MaskString(input); got != want {
				t.Fatalf("%s: got %q, want %q", label, got, want)
			}
		}
	}
	check("initial", m, specs)
	clone := m.Clone().(*piiMasker)
	if err := m.AddPattern("priority", "abcdef|abc", "$1"); err != nil {
		t.Fatal(err)
	}
	prioritySpecs := append([]multiRuleSpec{{"abcdef|abc", "$1"}}, specs...)
	check("prepend named rule", m, prioritySpecs)
	check("clone keeps old policy", clone, specs)
	if err := m.AddRule("[", "BROKEN", "regex"); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if err := m.AddPattern("priority", "[", "BROKEN"); err == nil {
		t.Fatal("invalid named regex accepted")
	}
	check("invalid updates preserve priority", m, prioritySpecs)
	m.RemovePattern("priority")
	check("named removal restores initial policy", m, specs)
	if err := m.RemoveRule("abc"); err != nil {
		t.Fatal(err)
	}
	check("earliest rule removed", m, specs[1:])
	if err := m.AddRule("abc", "LAST", "regex"); err != nil {
		t.Fatal(err)
	}
	check("append uses earlier start before rule order", m, []multiRuleSpec{{"bcdef", "SECOND"}, {"abc", "LAST"}})
	if err := clone.AddPattern("clone-only", "abcdef|abc", "CLONE"); err != nil {
		t.Fatal(err)
	}
	check("clone changes independently", clone, append([]multiRuleSpec{{"abcdef|abc", "CLONE"}}, specs...))
	check("original unchanged by clone", m, []multiRuleSpec{{"bcdef", "SECOND"}, {"abc", "LAST"}})
}

func FuzzMaskingMultiRuleOracle(f *testing.F) {
	seeds := []struct {
		input string
		rules [3]multiRuleSpec
	}{
		{"abcdefg!", [3]multiRuleSpec{{"abc", "X"}, {"cde", "$1"}, {"efg", "Z"}}},
		{"abcdef!", [3]multiRuleSpec{{"cdef", "Y"}, {"abc", "X"}, {"abcdef", "Z"}}},
		{"abc sensitive", [3]multiRuleSpec{{"abc", "sensitive"}, {"sensitive", "HIDDEN"}, {"!", ""}}},
		{"αβ\xff\xc0a", [3]multiRuleSpec{{"", "|"}, {"α|\uFFFD", "$1"}, {"a", ""}}},
		{"ab", [3]multiRuleSpec{{"^", "1"}, {`\b`, "2"}, {"ab", "X"}}},
		{"", [3]multiRuleSpec{{"^", "A"}, {"$", "B"}, {"", "C"}}},
		{"ab cd\n", [3]multiRuleSpec{{".*?", "$1"}, {"[a-z]+", "WORD"}, {`\s+`, " "}}},
		{strings.Repeat("abcdef!", 65), [3]multiRuleSpec{{"abc", "X"}, {"cde", "Y"}, {"ef", "Z"}}},
		{strings.Repeat(".", 4093) + "abcdefg!", [3]multiRuleSpec{{"abc", "X"}, {"cde", "Y"}, {"efg", "Z"}}},
	}
	for _, seed := range seeds {
		f.Add(seed.input, seed.rules[0].pattern, seed.rules[0].replacement, seed.rules[1].pattern, seed.rules[1].replacement, seed.rules[2].pattern, seed.rules[2].replacement)
	}
	f.Fuzz(func(t *testing.T, input, patternA, replacementA, patternB, replacementB, patternC, replacementC string) {
		if len(input) > 8192 {
			t.Skip()
		}
		specs := []multiRuleSpec{{patternA, replacementA}, {patternB, replacementB}, {patternC, replacementC}}
		rules := make([]multiRuleCompiled, len(specs))
		for i, spec := range specs {
			if len(spec.pattern) > 96 || len(spec.replacement) > 64 {
				t.Skip()
			}
			re, err := regexp.Compile(spec.pattern)
			if err != nil {
				t.Skip()
			}
			rules[i] = multiRuleCompiled{re, spec.replacement}
		}
		want := multiRuleOracle(input, rules)
		if got := newMultiRuleMasker(t, specs).MaskString(input); got != want {
			t.Fatalf("input %q, rules %#v: got %q, want %q", input, specs, got, want)
		}
	})
}
