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

// Package gen implements the halologgen generator: schema in, a
// compile-time-checked typed logging facade out. The CLI in cmd/halologgen
// is a thin shell over Run.
// Author: Admilson B. F. Cossa
package gen

import (
	"fmt"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"go.yaml.in/yaml/v4"
)

// Schema is the on-disk YAML shape.
type Schema struct {
	Package string  `yaml:"package"`
	Fields  []Field `yaml:"fields"`
}

// Field declares one loggable field.
type Field struct {
	Name  string `yaml:"name"`  // JSON key (required)
	Type  string `yaml:"type"`  // string|int|int64|float64|bool|error|any (required)
	Ident string `yaml:"ident"` // Go method name (optional; derived from Name)
}

// typeSpec maps a schema type to the builder mechanics it generates.
type typeSpec struct {
	goParam string // parameter type in the generated method
	keyed   string // keyed setter on core.Line / core.Context
}

var typeSpecs = map[string]typeSpec{
	"string":  {"string", "Str"},
	"int":     {"int", "Int"},
	"int64":   {"int64", "Int64"},
	"float64": {"float64", "Float64"},
	"bool":    {"bool", "Bool"},
	"error":   {"error", "Err"},
	"any":     {"interface{}", "Any"},
}

// reservedIdents are method names the facade itself uses; schema idents may
// not collide with them.
var reservedIdents = map[string]bool{
	"Wrap": true, "Unwrap": true, "With": true, "Logger": true,
	"Trace": true, "Debug": true, "Info": true, "Warn": true, "Error": true,
	"Msg": true, "Send": true, "Line": true, "Ctx": true,
}

// field is the validated, render-ready form.
type field struct {
	Name     string
	Ident    string
	KeyVar   string
	Param    string
	Keyed    string
	Fragment string // the frozen, generation-time-computed JSON key fragment
}

// Run loads the schema, validates it, and writes <package>.go and
// <package>_fragments_test.go into outDir, both gofmt-formatted.
func Run(schemaPath, outDir string) error {
	raw, err := os.ReadFile(schemaPath) //nolint:gosec // the schema path is the operator's explicit CLI input
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	var s Schema
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}
	fields, err := validate(&s)
	if err != nil {
		return err
	}

	data := map[string]interface{}{
		"Package": s.Package,
		"Schema":  filepath.Base(schemaPath),
		"Fields":  fields,
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := render(facadeTmpl, data, filepath.Join(outDir, s.Package+".go")); err != nil {
		return err
	}
	return render(fragmentsTmpl, data, filepath.Join(outDir, s.Package+"_fragments_test.go"))
}

// validate checks the schema and produces render-ready fields, computing
// each key's escaped JSON fragment ONCE, here — generation time is where
// escaping is decided and frozen.
func validate(s *Schema) ([]field, error) {
	if !token.IsIdentifier(s.Package) || token.IsExported(s.Package) {
		return nil, fmt.Errorf("package %q must be a valid, lower-case Go package name", s.Package)
	}
	if len(s.Fields) == 0 {
		return nil, fmt.Errorf("schema declares no fields")
	}

	fields := make([]field, 0, len(s.Fields))
	seenName := map[string]bool{}
	seenIdent := map[string]bool{}
	for i, f := range s.Fields {
		if f.Name == "" {
			return nil, fmt.Errorf("field %d: name is required", i)
		}
		if seenName[f.Name] {
			return nil, fmt.Errorf("field %q: duplicate name", f.Name)
		}
		seenName[f.Name] = true

		spec, ok := typeSpecs[f.Type]
		if !ok {
			return nil, fmt.Errorf("field %q: unknown type %q (want %s)", f.Name, f.Type, typeList())
		}

		ident := f.Ident
		if ident == "" {
			ident = deriveIdent(f.Name)
		}
		if !token.IsIdentifier(ident) || !token.IsExported(ident) {
			return nil, fmt.Errorf("field %q: ident %q must be a valid exported Go identifier", f.Name, ident)
		}
		if reservedIdents[ident] {
			return nil, fmt.Errorf("field %q: ident %q collides with a facade method", f.Name, ident)
		}
		if seenIdent[ident] {
			return nil, fmt.Errorf("field %q: duplicate ident %q", f.Name, ident)
		}
		seenIdent[ident] = true

		fields = append(fields, field{
			Name:     f.Name,
			Ident:    ident,
			KeyVar:   "key" + ident,
			Param:    spec.goParam,
			Keyed:    spec.keyed,
			Fragment: string(jsonfmt.KeyFragment(f.Name)),
		})
	}
	return fields, nil
}

// deriveIdent turns a JSON key like "user_id" or "http.status-code" into an
// exported Go identifier like UserID-style CamelCase (initialisms are kept
// simple: every separator starts a new upper-cased word).
func deriveIdent(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ' || r == '/':
			upper = true
		case upper:
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func typeList() string {
	names := make([]string, 0, len(typeSpecs))
	for k := range typeSpecs {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

// render executes tmpl, gofmt-formats the result, and writes it.
func render(tmpl *template.Template, data interface{}, path string) error {
	var raw strings.Builder
	if err := tmpl.Execute(&raw, data); err != nil {
		return fmt.Errorf("render %s: %w", filepath.Base(path), err)
	}
	formatted, err := format.Source([]byte(raw.String()))
	if err != nil {
		return fmt.Errorf("gofmt %s (generator bug): %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, formatted, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

var facadeTmpl = template.Must(template.New("facade").Parse(`// Code generated by halologgen from {{.Schema}}. DO NOT EDIT.

// Package {{.Package}} is a compile-time-checked logging facade: every
// loggable field is a typed method, so a misspelled key or wrong-typed
// value fails the build instead of corrupting a log line. Keys are
// pre-declared once; their escaped JSON fragments are frozen by the
// generated fragments test.
package {{.Package}}

import (
	"github.com/go-gen-ecosystem/halolog"
	"github.com/go-gen-ecosystem/halolog/core"
)

// Pre-declared keys — escaping computed at generation time, pinned by test.
var (
{{- range .Fields}}
	{{.KeyVar}} = halolog.Key({{printf "%q" .Name}})
{{- end}}
)

// Logger wraps a core logger with this schema's typed vocabulary.
type Logger struct{ l *core.Logger }

// Wrap adapts a configured core logger to the schema facade.
func Wrap(l *core.Logger) Logger { return Logger{l: l} }

// Unwrap exposes the underlying core logger (escape hatch).
func (w Logger) Unwrap() *core.Logger { return w.l }

// With opens a schema-typed context builder; bound fields are encoded once
// and cost one memcpy per line thereafter.
func (w Logger) With() Ctx { return Ctx{c: w.l.With()} }

// Ctx accumulates schema-typed bound fields for a child logger.
type Ctx struct{ c core.Context }

{{range .Fields}}
// {{.Ident}} binds the {{printf "%q" .Name}} field.
func (c Ctx) {{.Ident}}(v {{.Param}}) Ctx { return Ctx{c: c.c.{{.Keyed}}({{.KeyVar}}, v)} }
{{end}}
// Logger derives the child logger carrying the bound fields.
func (c Ctx) Logger() Logger { return Logger{l: c.c.Logger()} }

// Level-first lines: a disabled level costs a single check.

// Trace opens a TRACE-level line.
func (w Logger) Trace() Line { return Line{ln: w.l.TraceLine()} }

// Debug opens a DEBUG-level line.
func (w Logger) Debug() Line { return Line{ln: w.l.DebugLine()} }

// Info opens an INFO-level line.
func (w Logger) Info() Line { return Line{ln: w.l.InfoLine()} }

// Warn opens a WARN-level line.
func (w Logger) Warn() Line { return Line{ln: w.l.WarnLine()} }

// Error opens an ERROR-level line.
func (w Logger) Error() Line { return Line{ln: w.l.ErrorLine()} }

// Line is a schema-typed level-first line builder.
type Line struct{ ln core.Line }

{{range .Fields}}
// {{.Ident}} adds the {{printf "%q" .Name}} field.
func (e Line) {{.Ident}}(v {{.Param}}) Line { return Line{ln: e.ln.{{.Keyed}}({{.KeyVar}}, v)} }
{{end}}
// Msg completes the line with a message and writes it.
func (e Line) Msg(msg string) { e.ln.Msg(msg) }

// Send completes the line with an empty message.
func (e Line) Send() { e.ln.Send() }
`))

var fragmentsTmpl = template.Must(template.New("fragments").Parse(`// Code generated by halologgen from {{.Schema}}. DO NOT EDIT.

package {{.Package}}

import (
	"bytes"
	"testing"
)

// TestKeyFragmentsFrozen pins every key's escaped JSON fragment to the form
// computed at generation time. If the runtime escaper and the generator ever
// disagree, this fails the consumer's build — escaping is decided once, at
// generation, and reviewed like any other code.
func TestKeyFragmentsFrozen(t *testing.T) {
	pins := []struct {
		name     string
		fragment string
		got      []byte
	}{
{{- range .Fields}}
		{ {{printf "%q" .Name}}, {{printf "%q" .Fragment}}, {{.KeyVar}}.JSONFragment},
{{- end}}
	}
	for _, p := range pins {
		if !bytes.Equal(p.got, []byte(p.fragment)) {
			t.Fatalf("key %q: fragment drifted from generation-time form: got %q want %q",
				p.name, p.got, p.fragment)
		}
	}
}
`))
