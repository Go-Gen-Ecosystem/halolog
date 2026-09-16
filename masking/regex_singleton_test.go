package masking

import (
	"math/rand"
	"regexp"
	"testing"
)

// The oracle compiles the original source independently and lets regexp decide
// match positions and multiplicity. It does not reuse helper patterns or their
// eligibility analysis, so a wrong context or match preference is observable.
func checkRegexSingletonOracle(t *testing.T, input, pattern string, rule regexRule, original *regexp.Regexp) {
	t.Helper()
	if rule.prefix == nil {
		// Nullable rules and unavailable optional helpers must retain the full
		// standard-library behavior, including zero-width match suppression.
		pm := &piiMasker{}
		snapshot := &maskerSnapshot{regexRules: []regexRule{rule}}
		if got, want := pm.maskString(input, snapshot), original.ReplaceAllLiteralString(input, rule.replace); got != want {
			t.Fatalf("fallback pattern=%q input=%q: got %q, want %q", pattern, input, got, want)
		}
		return
	}

	want := original.FindAllStringIndex(input, -1)
	start, end, count := rule.singleton(input)
	if count != min(len(want), 2) {
		t.Fatalf("pattern=%q input=%q: singleton count=%d, oracle matches=%v", pattern, input, count, want)
	}
	if count == 1 && (start != want[0][0] || end != want[0][1]) {
		t.Fatalf("pattern=%q input=%q: singleton=[%d %d], oracle=%v", pattern, input, start, end, want[0])
	}
}

func TestRegexSingletonDifferentialOracle(t *testing.T) {
	atoms := []string{
		"", "a", "b", "ab", "a|ab", "ab|a", ".", "[ab]", "[^a]",
		`\w`, `\d`, `\b`, `\B`, "^", "$", `\A`, `\z`, "α", "β", "\uFFFD",
		"(?i:a)", "(?m:^)", "(?m:$)",
	}
	patterns := append([]string(nil), atoms...)
	for _, a := range atoms {
		for _, repetition := range []string{"*", "+", "?", "*?", "+?", "??", "{1,3}", "{0,3}?"} {
			patterns = append(patterns, "(?:"+a+")"+repetition)
		}
		for _, b := range atoms {
			patterns = append(patterns, "(?:"+a+")(?:"+b+")", "(?:"+a+")|(?:"+b+")")
		}
	}
	seedInputs := []string{
		"", "a", "ab", "abc", "aaa", "abaa aa", "αβ", "Ā\x80", "\xffa",
		"\xffabc\xc0|\xfe", "a\nb", "\na\n", "aa\nab", "ab ab", "\r\n",
		"a b! a", "éà a", "0a_",
	}
	inputs := make([]string, 0, len(seedInputs)+150)
	inputs = append(inputs, seedInputs...)
	rng := rand.New(rand.NewSource(27124))
	alphabet := []byte{'a', 'b', '0', ' ', '\n', 0xff, 0xc0, 0xc4, 0x80, 0xce, 0xb1}
	for range 150 {
		input := make([]byte, rng.Intn(24))
		for i := range input {
			input[i] = alphabet[rng.Intn(len(alphabet))]
		}
		inputs = append(inputs, string(input))
	}

	for _, pattern := range patterns {
		original := regexp.MustCompile(pattern)
		rule, err := newRegexRule(pattern, "$1${name}$$")
		if err != nil {
			t.Fatalf("valid original %q rejected: %v", pattern, err)
		}
		for _, input := range inputs {
			checkRegexSingletonOracle(t, input, pattern, rule, original)
		}
	}
	t.Logf("checked %d original patterns across %d inputs (%d pairs)", len(patterns), len(inputs), len(patterns)*len(inputs))
}

func TestRegexSingletonQuotedAndContextRegressions(t *testing.T) {
	tests := []struct {
		name, pattern, input string
		fast                 bool
	}{
		{"unclosed_empty_quote", `\Q`, "a", false},
		{"unclosed_literal_quote", `\Qabc`, "!abc?", true},
		{"unclosed_quote_metacharacters", `\Q(a|b)*$`, "!(a|b)*$?", true},
		{"closed_quote_with_repetition", `\Qa.b\E+`, "!a.bb?", true},
		{"named_capture", `(?P<secret>ab|a)`, "!ab?", true},
		{"scoped_case_flags", `(?i)a(?-i)b`, "!Ab?", true},
		{"ungreedy_flag", `(?U)a+`, "aaa", true},
		{"word_boundary_previous_duplicate", `\ba`, "xa a", true},
		{"end_anchor_previous_duplicate", `a$`, "aa", true},
		{"line_anchor_previous_duplicate", `(?m)^a`, "ba\na", true},
		{"invalid_byte_inside_earlier_valid_rune", "\uFFFD", "Ā\x80", true},
		{"invalid_preceding_rune", `\ba`, "\xffa\xc0a", true},
		{"word_boundary_context", `\ba`, "aab", true},
		{"absolute_start_context", `\Aa`, "aa", true},
		{"line_start_context", `^a`, "aa", true},
		{"multiline_second_match", `(?m)^a`, "a\na", true},
		{"absolute_end", `a\z`, "aa", true},
		{"short_first_alternative", "a|ab", "ab", true},
		{"long_first_alternative", "ab|a", "ab", true},
		{"lazy_nonempty", "a+?", "aaa", true},
		{"empty_expression", "", "αβ\xff", false},
		{"nullable_word_boundary", `\b`, "ab", false},
		{"nullable_nonword_boundary", `\B`, "ab", false},
		{"nullable_start", "^", "ab", false},
		{"nullable_end", "$", "ab", false},
		{"nullable_absolute_start", `\A`, "", false},
		{"nullable_absolute_end", `\z`, "", false},
		{"nullable_alternative", "a|", "ab", false},
		{"nullable_lazy", ".*?", "αβ\xff", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := regexp.MustCompile(tc.pattern)
			rule, err := newRegexRule(tc.pattern, "$1\\0")
			if err != nil {
				t.Fatalf("valid original rejected: %v", err)
			}
			if got := rule.prefix != nil; got != tc.fast {
				t.Fatalf("fast path enabled=%v, want %v", got, tc.fast)
			}
			checkRegexSingletonOracle(t, tc.input, tc.pattern, rule, original)
		})
	}
}

func FuzzRegexSingleton(f *testing.F) {
	for _, input := range []string{"", "ab ab\n", "αβ\xff\xc0a", "Ā\x80", "xa a", "aab"} {
		for _, pattern := range []string{`\b`, `(?m)^a|a$`, `\A.|.\z`, `a|ab`, `a+?`, `a*?`, `(?:α|�|a)+`, `\Q`, `\Q(a|b)*$`, `(?P<secret>a)(?i:b)`} {
			f.Add(input, pattern)
		}
	}
	f.Fuzz(func(t *testing.T, input, pattern string) {
		if len(input) > 4096 || len(pattern) > 128 {
			t.Skip()
		}
		original, err := regexp.Compile(pattern)
		if err != nil {
			t.Skip()
		}
		rule, err := newRegexRule(pattern, "$1${name}$$")
		if err != nil {
			t.Fatalf("valid original %q rejected: %v", pattern, err)
		}
		checkRegexSingletonOracle(t, input, pattern, rule, original)
	})
}
