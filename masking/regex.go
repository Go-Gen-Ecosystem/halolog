package masking

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"
)

type regexRule struct {
	re      *regexp.Regexp
	replace string
	boxed   interface{} // immutable replacement, boxed once on the configuration path
	prefix  *regexp.Regexp
	context *regexp.Regexp
}

func newRegexRule(pattern, replacement string) (regexRule, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return regexRule{}, err
	}
	rule := regexRule{re: re, replace: replacement, boxed: replacement}
	ast, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil || mayMatchEmpty(ast) {
		return rule, nil
	}
	// Normalize before wrapping: a valid pattern can end in an unclosed \Q
	// literal quote, or contain flags that must not affect the helper prefix.
	normalized := ast.String()
	prefix, prefixErr := regexp.Compile(`\A(?s:.*?)(?:` + normalized + `)`)
	context, contextErr := regexp.Compile(`(?s:.)(?:` + normalized + `)`)
	// A valid original near regexp's complexity limits may not admit wrappers.
	// Keep the original behavior instead of rejecting or weakening the rule.
	if prefixErr == nil && contextErr == nil {
		rule.prefix, rule.context = prefix, context
	}
	return rule, nil
}

// mayMatchEmpty conservatively recognizes zero-width possibilities. Assertions
// may be impossible together, but a false positive only selects the fallback.
func mayMatchEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return false
	case syntax.OpLiteral:
		return len(re.Rune) == 0
	case syntax.OpCapture, syntax.OpPlus:
		return mayMatchEmpty(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !mayMatchEmpty(sub) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if mayMatchEmpty(sub) {
				return true
			}
		}
		return false
	case syntax.OpRepeat:
		return re.Min == 0 || mayMatchEmpty(re.Sub[0])
	default:
		return true
	}
}

// singleton is used only for rules proven to consume input. Count 2 requests
// FindAllStringIndex; 0 or 1 gives the complete result without index slices.
// There are at most three scans, independent of the number of matches. In
// particular, this does not repeatedly scan to EOF for every dense match.
func (r regexRule) singleton(s string) (start, end, count int) {
	m := r.re.FindString(s)
	if m == "" {
		return 0, 0, 0
	}
	if len(m) == len(s) {
		return 0, len(s), 1
	}
	// The lazy anchored prefix picks the same leftmost-first match. Subtracting
	// its length is safe; strings.Index(m) would not preserve boundary assertions.
	end = len(r.prefix.FindString(s))
	start = end - len(m)
	if end == len(s) {
		return start, end, 1
	}
	// Retain the preceding rune so ^, \A, multiline and word boundaries see
	// the original context. DecodeLastRune also handles invalid UTF-8 bytes.
	_, before := utf8.DecodeLastRuneInString(s[:end])
	if r.context.MatchString(s[end-before:]) {
		return 0, 0, 2
	}
	return start, end, 1
}

// maskString matches every rule against the original input. Replacements are
// literal. Matches are ordered by start, with rule order breaking ties. Overlap
// is redacted as a union using the first replacement, so a later overlapping
// match cannot leave an uncovered secret suffix. Adjacent matches stay separate.
//
// There is no cache of raw secrets and no global intern pool. No-match inputs
// return unchanged; matching inputs may allocate owned output. Costs are bounded
// by the input and configured matches, not by process lifetime or input diversity.
func (pm *piiMasker) maskString(s string, snap *maskerSnapshot) string {
	masked, _ := pm.maskStringResult(s, snap)
	return masked
}

// The optional box belongs to immutable configuration, never to an input cache.
// It lets legacy field storage reuse a constant replacement without boxing it
// on every emission. Dynamically assembled results always own their bytes.
func (pm *piiMasker) maskStringResult(s string, snap *maskerSnapshot) (string, interface{}) {
	if snap == nil || len(snap.regexRules) == 0 {
		return s, nil
	}
	type match struct {
		start, end  int
		replacement string
		boxed       interface{}
	}
	var inline [32]match
	matches := inline[:0]
	for _, rule := range snap.regexRules {
		re := rule.re
		if re == nil {
			continue
		}
		if rule.prefix != nil {
			start, end, count := rule.singleton(s)
			if count == 0 {
				continue
			}
			if count == 1 {
				matches = append(matches, match{start, end, rule.replace, rule.boxed})
				continue
			}
		}
		for _, idx := range re.FindAllStringIndex(s, -1) {
			matches = append(matches, match{idx[0], idx[1], rule.replace, rule.boxed})
		}
	}
	if len(matches) == 0 {
		return s, nil
	}
	slices.SortStableFunc(matches, func(a, b match) int { return a.start - b.start })
	// Merge first so a complete redaction can return its configured constant.
	merged := 0
	for i := 0; i < len(matches); {
		m := matches[i]
		i++
		for i < len(matches) && matches[i].start < m.end {
			if matches[i].end > m.end {
				m.end = matches[i].end
			}
			i++
		}
		matches[merged] = m
		merged++
	}
	matches = matches[:merged]
	if len(matches) == 1 && matches[0].start == 0 && matches[0].end == len(s) {
		return matches[0].replacement, matches[0].boxed
	}
	// Builder.String retains the buffer. Reserve only the final output, not a
	// possibly huge redacted input; also avoid growth for expanding replacements.
	outputLen := len(s)
	for _, m := range matches {
		outputLen -= m.end - m.start
	}
	for _, m := range matches {
		if len(m.replacement) > int(^uint(0)>>1)-outputLen {
			panic("masking: output size exceeds int range")
		}
		outputLen += len(m.replacement)
	}
	var out strings.Builder
	out.Grow(outputLen)
	end := 0
	for _, m := range matches {
		out.WriteString(s[end:m.start])
		out.WriteString(m.replacement)
		end = m.end
	}
	out.WriteString(s[end:])
	return out.String(), nil
}
