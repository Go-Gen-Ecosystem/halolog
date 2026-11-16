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

// Package gen — generator determinism (regenerate must byte-match the
// committed example) and schema validation coverage.
// @author Admilson B. F. Cossa

package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v4"
)

const exampleDir = "../../../../examples/applog"

// TestRegenerateMatchesCommittedExample regenerates the example facade into
// a temp dir and byte-compares it with the committed output: the generator
// is deterministic, and the committed example can never drift from its
// schema without this failing.
func TestRegenerateMatchesCommittedExample(t *testing.T) {
	tmp := t.TempDir()
	if err := Run(filepath.Join(exampleDir, "schema.yaml"), tmp); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"applog.go", "applog_fragments_test.go"} {
		got, err := os.ReadFile(filepath.Join(tmp, name)) //nolint:gosec // test-owned paths
		if err != nil {
			t.Fatalf("read regenerated %s: %v", name, err)
		}
		want, err := os.ReadFile(filepath.Join(exampleDir, name)) //nolint:gosec // test-owned paths
		if err != nil {
			t.Fatalf("read committed %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: regenerated output differs from the committed example — "+
				"rerun `go run ./cmd/halologgen -schema examples/applog/schema.yaml -out examples/applog`", name)
		}
	}
}

// TestValidateRejectsBadSchemas covers every validation rule.
func TestValidateRejectsBadSchemas(t *testing.T) {
	cases := []struct {
		name string
		yml  string
	}{
		{"bad package", "package: Applog\nfields:\n  - {name: a, type: string}\n"},
		{"no fields", "package: applog\nfields: []\n"},
		{"missing name", "package: applog\nfields:\n  - {type: string}\n"},
		{"duplicate name", "package: applog\nfields:\n  - {name: a, type: string}\n  - {name: a, type: int}\n"},
		{"unknown type", "package: applog\nfields:\n  - {name: a, type: uuid}\n"},
		{"unexported ident", "package: applog\nfields:\n  - {name: a, type: string, ident: little}\n"},
		{"reserved ident", "package: applog\nfields:\n  - {name: a, type: string, ident: Msg}\n"},
		{"duplicate ident", "package: applog\nfields:\n  - {name: a_b, type: string}\n  - {name: a.b, type: int}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Schema
			if err := yaml.Unmarshal([]byte(tc.yml), &s); err != nil {
				t.Fatalf("test yaml invalid: %v", err)
			}
			if _, err := validate(&s); err == nil {
				t.Fatalf("schema accepted, want rejection")
			}
		})
	}
}

// TestDeriveIdent pins the name→identifier derivation.
func TestDeriveIdent(t *testing.T) {
	cases := map[string]string{
		"user_id":          "UserId",
		"http.status-code": "HttpStatusCode",
		"a b/c":            "ABC",
		"simple":           "Simple",
	}
	for in, want := range cases {
		if got := deriveIdent(in); got != want {
			t.Errorf("deriveIdent(%q) = %q, want %q", in, got, want)
		}
	}
}
