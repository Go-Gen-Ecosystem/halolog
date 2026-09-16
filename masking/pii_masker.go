// Copyright 2025 Admilson B. F. Cossa
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package masking provides PII masking functionality
// Author: Admilson B. F. Cossa

package masking

import (
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/go-gen-ecosystem/halolog/registry"
	"github.com/go-gen-ecosystem/halolog/types"
)

// maskerSnapshot is published atomically and never mutated after publication.
// The registry is separately synchronized by its own API.
type maskerSnapshot struct {
	fieldRules map[string]interface{} // preboxed replacement strings
	regexRules []regexRule
}

// Registration ownership exists only on the configuration path. Equal regexes
// can belong to different named policies or to an unnamed/built-in rule.
type storedMaskingRule struct {
	types.MaskingRule
	name  string
	named bool // an empty name is still distinct from an unnamed rule
}

type piiMasker struct {
	activeState            atomic.Pointer[maskerSnapshot]
	configMu               sync.Mutex
	storedRules            []storedMaskingRule
	patternNames           map[string]string
	sensitiveFieldRegistry *registry.SensitiveFieldRegistry
}

// NewPIIMasker creates a masker with the built-in rules.
func NewPIIMasker() types.PIIMasker { return NewPIIMaskerWithRegistry(nil) }

// NewPIIMaskerWithRegistry adds externally managed field-name rules.
// Registry rules take precedence over local field rules and regex matching.
func NewPIIMaskerWithRegistry(r *registry.SensitiveFieldRegistry) types.PIIMasker {
	pm := &piiMasker{patternNames: make(map[string]string), sensitiveFieldRegistry: r}
	pm.addDefaultPatterns()
	return pm
}

// Apply requires exclusive ownership of the entry until fan-out completes.
func (pm *piiMasker) Apply(entry *types.LogEntry) {
	if entry == nil {
		return
	}
	snap := pm.activeState.Load()
	if snap == nil || (len(snap.fieldRules) == 0 && len(snap.regexRules) == 0 && pm.sensitiveFieldRegistry == nil) {
		return
	}
	pm.maskFields(entry.StaticFields[:min(max(entry.StaticFieldCount, 0), len(entry.StaticFields))], snap)
	pm.maskFields(entry.Fields, snap)
	pm.maskFields(entry.StaticContext[:min(max(entry.StaticContextCount, 0), len(entry.StaticContext))], snap)
	pm.maskFields(entry.Context, snap)
	if entry.IndexedStore != nil {
		entry.IndexedStore.TransformValues(func(key, value string) string {
			f := types.TypedFieldData{Key: key, Val: types.StringValue(value)}
			pm.maskFieldFast(&f, snap)
			return f.Val.String
		})
	}
	entry.Message = pm.maskString(entry.Message, snap)
}

func (pm *piiMasker) MaskField(field *types.TypedFieldData) *types.TypedFieldData {
	snap := pm.activeState.Load()
	return pm.maskFieldFast(field, snap)
}

func (pm *piiMasker) MaskFields(fields []types.TypedFieldData) []types.TypedFieldData {
	snap := pm.activeState.Load()
	pm.maskFields(fields, snap)
	return fields
}

func (pm *piiMasker) maskFields(fields []types.TypedFieldData, snap *maskerSnapshot) {
	for i := range fields {
		pm.maskFieldFast(&fields[i], snap)
	}
}

func (pm *piiMasker) MaskString(input string) string {
	snap := pm.activeState.Load()
	return pm.maskString(input, snap)
}

// setMaskedString removes stale values from both storage forms. Field-rule
// replacements are preboxed during configuration, so this path does not allocate.
func setMaskedString(field *types.TypedFieldData, value string, boxed interface{}) {
	field.Val = types.StringValue(value)
	field.Value = boxed
	field.Type = types.TypedFieldString
}

func (pm *piiMasker) maskFieldFast(field *types.TypedFieldData, snap *maskerSnapshot) *types.TypedFieldData {
	if field == nil || snap == nil {
		return field
	}
	// Prefer an explicit legacy rewrite and remove the typed original so no
	// exporter can resurrect it. Nil cannot represent an explicit legacy rewrite.
	if field.Value != nil && field.Val.Kind != types.KindUnknown {
		field.Val = types.FieldValue{}
	}
	key := field.Key
	if key == "" && field.KeyDesc != nil {
		key = field.KeyDesc.Name
	}
	if pm.sensitiveFieldRegistry != nil {
		if mask, ok := pm.sensitiveFieldRegistry.GetMaskInterface(key); ok {
			setMaskedString(field, mask.(string), mask)
			return field
		}
		if mask := pm.sensitiveFieldRegistry.GetDefaultUnknownMaskInterface(); mask != nil {
			setMaskedString(field, mask.(string), mask)
			return field
		}
	}
	if mask, ok := snap.fieldRules[key]; ok {
		setMaskedString(field, mask.(string), mask)
		return field
	}
	if len(snap.regexRules) == 0 {
		return field
	}
	// Value may contain a previous middleware rewrite. Otherwise typed storage
	// wins, matching the formatter. Arbitrary Any values are not stringified.
	var s string
	var ok bool
	if field.Value != nil {
		s, ok = field.Value.(string)
	} else {
		switch field.Val.Kind {
		case types.KindString, types.KindError:
			s, ok = field.Val.String, true
		case types.KindAny:
			s, ok = field.Val.Any.(string)
		}
	}
	if ok {
		if masked, boxed := pm.maskStringResult(s, snap); masked != s {
			if boxed == nil {
				boxed = masked
			}
			setMaskedString(field, masked, boxed)
		}
	}
	return field
}

// -----------------------------
// 4. Configuration & Compilation
// -----------------------------

func (pm *piiMasker) AddRule(pattern string, replace string, ruleType string) error {
	if err := validateRule(pattern, ruleType); err != nil {
		return err
	}
	pm.configMu.Lock()
	defer pm.configMu.Unlock()

	return pm.publishRules(append(pm.storedRules, storedMaskingRule{MaskingRule: types.MaskingRule{
		Type:    ruleType,
		Pattern: pattern,
		Replace: replace,
	}}))
}

// RemoveRule removes all registrations matching pattern, including named ones.
// RemovePattern instead removes only the registration belonging to one name.
func (pm *piiMasker) RemoveRule(pattern string) error {
	pm.configMu.Lock()
	defer pm.configMu.Unlock()

	// Find and remove the rule
	newRules := make([]storedMaskingRule, 0, len(pm.storedRules))
	for _, rule := range pm.storedRules {
		if rule.Pattern != pattern {
			newRules = append(newRules, rule)
		}
	}
	if err := pm.publishRules(newRules); err != nil {
		return err
	}
	for name, registered := range pm.patternNames {
		if registered == pattern {
			delete(pm.patternNames, name)
		}
	}
	return nil
}

// AddPattern adds or replaces one named rule, giving it highest regex priority.
// Updating a name replaces its previous registration, not other equal regexes.
func (pm *piiMasker) AddPattern(name, patternStr, mask string) error {
	if err := validateRule(patternStr, "regex"); err != nil {
		return err
	}
	pm.configMu.Lock()
	defer pm.configMu.Unlock()

	newRule := storedMaskingRule{MaskingRule: types.MaskingRule{
		Type:    "regex",
		Pattern: patternStr,
		Replace: mask,
	}, name: name, named: true}
	newRules := make([]storedMaskingRule, 1, len(pm.storedRules)+1)
	newRules[0] = newRule
	for _, rule := range pm.storedRules {
		if !rule.named || rule.name != name {
			newRules = append(newRules, rule)
		}
	}
	if err := pm.publishRules(newRules); err != nil {
		return err
	}
	if pm.patternNames == nil {
		pm.patternNames = make(map[string]string)
	}
	pm.patternNames[name] = patternStr
	return nil
}

// RemovePattern removes only the registration owned by name. Equal patterns
// registered under another name or through AddRule remain active.
func (pm *piiMasker) RemovePattern(name string) {
	pm.configMu.Lock()
	defer pm.configMu.Unlock()

	if _, exists := pm.patternNames[name]; exists {
		newRules := make([]storedMaskingRule, 0, len(pm.storedRules))
		for _, rule := range pm.storedRules {
			if !rule.named || rule.name != name {
				newRules = append(newRules, rule)
			}
		}
		if pm.publishRules(newRules) == nil {
			delete(pm.patternNames, name)
		}
	}
}

// GetPatterns returns all pattern names
func (pm *piiMasker) GetPatterns() []string {
	pm.configMu.Lock()
	defer pm.configMu.Unlock()

	patterns := make([]string, 0, len(pm.patternNames))
	for name := range pm.patternNames {
		patterns = append(patterns, name)
	}
	return patterns
}

// Clone shares immutable compiled state and the externally managed registry.
// Its rule configuration and pattern names can subsequently change independently.
func (pm *piiMasker) Clone() types.PIIMasker {
	pm.configMu.Lock()
	defer pm.configMu.Unlock()
	cp := &piiMasker{
		storedRules:            append([]storedMaskingRule(nil), pm.storedRules...),
		patternNames:           make(map[string]string, len(pm.patternNames)),
		sensitiveFieldRegistry: pm.sensitiveFieldRegistry,
	}
	for k, v := range pm.patternNames {
		cp.patternNames[k] = v
	}
	cp.activeState.Store(pm.activeState.Load())
	return cp
}

func validateRule(pattern, kind string) error {
	switch kind {
	case "field":
		return nil
	case "regex":
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("masking regex: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported masking rule type %q", kind)
	}
}

// updateSnapshot is called under configMu. Compilation belongs to the cold
// path; readers load exactly one immutable policy for an entire Apply call.
func (pm *piiMasker) updateSnapshot() error {
	return pm.publishRules(pm.storedRules)
}

// publishRules commits configuration only after the complete policy compiles.
// Callers hold configMu; emission still loads the same immutable snapshot.
func (pm *piiMasker) publishRules(rules []storedMaskingRule) error {
	snap := &maskerSnapshot{fieldRules: make(map[string]interface{})}
	for _, rule := range rules {
		switch rule.Type {
		case "field":
			snap.fieldRules[rule.Pattern] = rule.Replace
		case "regex":
			compiled, err := newRegexRule(rule.Pattern, rule.Replace)
			if err != nil {
				return err
			}
			snap.regexRules = append(snap.regexRules, compiled)
		default:
			return fmt.Errorf("unsupported masking rule type %q", rule.Type)
		}
	}
	pm.storedRules = rules
	pm.activeState.Store(snap)
	return nil
}

// addDefaultPatterns compiles the trusted built-in rules once at construction.
func (pm *piiMasker) addDefaultPatterns() {
	// {pattern, replacement, type}
	defaultRules := [][3]string{
		// CRITICAL: Use field-name matching for password fields (fast path)
		{"password", "***PASSWORD***", "field"},
		{"passwd", "***PASSWORD***", "field"},
		{"pwd", "***PASSWORD***", "field"},
		{"pass", "***PASSWORD***", "field"},
		{"secret", "***SECRET***", "field"},
		{"token", "***TOKEN***", "field"},
		{"api_key", "***API_KEY***", "field"},
		{"apikey", "***API_KEY***", "field"},

		// Regex rules for message masking
		// Email
		{`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`, "[EMAIL]", "regex"},
		// Credit Card (common formats)
		{`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`, "[CREDIT_CARD]", "regex"},
		// SSN
		{`\b\d{3}-\d{2}-\d{4}\b`, "[SSN]", "regex"},
		// Phone - handles multiple formats: (555) 123-4567, 555-123-4567, 555.123.4567, 5551234567
		{`(?:\(\d{3}\)\s?\d{3}[-.]?\d{4}|\b\d{3}[-.]?\d{3}[-.]?\d{4}\b)`, "[PHONE]", "regex"},
		// IP Address
		{`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`, "[IP_ADDRESS]", "regex"},

		// API Keys (more specific patterns first)
		{`\bsk-[a-zA-Z0-9]{10,}\b`, "***API_KEY***", "regex"},
		{`\bapi[_-]?key[:\s]*[a-zA-Z0-9]{8,}\b`, "***API_KEY***", "regex"},

		// Password in messages (common patterns)
		{`\bpassword:\s+[a-zA-Z0-9]{5,}\b`, "Password: ***PASSWORD***", "regex"},
		{`\bpass:\s+[a-zA-Z0-9]{5,}\b`, "Pass: ***PASSWORD***", "regex"},
		{`\bPassword:\s+[a-zA-Z0-9]{5,}\b`, "Password: ***PASSWORD***", "regex"},
		{`\bPass:\s+[a-zA-Z0-9]{5,}\b`, "Pass: ***PASSWORD***", "regex"},

		// High entropy tokens (alphanumeric strings with high entropy) - less specific, so comes last
		{`\b[a-zA-Z0-9]{12,}\b`, "REDACTED", "regex"},
		{`\b[a-zA-Z0-9!@#$%^&*()_+=\-{}\[\]:;"'|\?/.,<>~]{12,}\b`, "REDACTED", "regex"},
		{`\b[a-zA-Z][a-zA-Z0-9]{11,}\b`, "REDACTED", "regex"},

		// Password fields (common field names)
		{`password`, "***PASSWORD***", "field"},
		{`passwd`, "***PASSWORD***", "field"},
		{`pwd`, "***PASSWORD***", "field"},
		{`pass`, "***PASSWORD***", "field"},
	}

	for _, r := range defaultRules {
		pm.storedRules = append(pm.storedRules, storedMaskingRule{MaskingRule: types.MaskingRule{Pattern: r[0], Replace: r[1], Type: r[2]}})
	}
	if err := pm.updateSnapshot(); err != nil {
		panic(err)
	} // invalid built-in rule is a programming error
}
