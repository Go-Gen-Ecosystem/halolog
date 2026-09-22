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

// Package otelbridge — masking must survive the export boundary.
//
// These run the real piiMasker, not a stub: masking rewrites a field after the
// builder wrote it, and the adapter has to read back the rewrite rather than
// the typed original. A leak here ships the PII off the host.
// @author Admilson B. F. Cossa
package otelbridge

import (
	"bytes"
	"testing"

	jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
	"github.com/go-gen-ecosystem/halolog/adapters/outputs/console"
	"github.com/go-gen-ecosystem/halolog/core"
	"github.com/go-gen-ecosystem/halolog/masking"
	"github.com/go-gen-ecosystem/halolog/types"
)

const (
	secret    = "hunter2"
	redacted  = "***PASSWORD***"
	email     = "someone@example.com"
	emailMask = "[EMAIL]"
)

// newMaskedLogger builds the fan-out with masking on. NewPIIMasker ships the
// default rule set, which includes the "password" field rule and the email
// regex used below.
func newMaskedLogger(buf *bytes.Buffer, rec *recorder) *core.Logger {
	return core.NewLogger(core.Config{
		Level:         types.InfoLevel,
		EnableMasking: true,
		Masker:        masking.NewPIIMasker(),
		Adapters: []types.Adapter{
			console.NewWithWriter(buf, jsonfmt.NewJsonFormatter()),
			NewAdapter("halolog/otelbridge_test", WithLoggerProvider(rec)),
		},
	})
}

// builders covers every way a value can reach an entry, since masking has to
// hold for all of them and they use different storage internally.
var builders = []struct {
	name string
	emit func(l *core.Logger, key, value string)
}{
	{"typed builder", func(l *core.Logger, k, v string) { l.Typed().WithString(k, v).Info("login") }},
	{"interface-boxed builder", func(l *core.Logger, k, v string) { l.WithField(k, v).Info("login") }},
	{"bound context", func(l *core.Logger, k, v string) { l.With().WithString(k, v).Logger().Info("login") }},
}

// The invariant is that every exporter observes the post-mask value. These
// assert the redacted value itself on both halves of the documented fan-out,
// and say nothing about which storage slot holds it — a repair that makes
// either slot canonical passes unchanged.
func TestFanout_EveryExporterSeesTheMaskedValue(t *testing.T) {
	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			var buf bytes.Buffer
			rec := &recorder{}
			b.emit(newMaskedLogger(&buf, rec), "password", secret)

			if got := rec.only(t).attrs()["password"].AsString(); got != redacted {
				t.Fatalf("OTLP record exported %q, want %q", got, redacted)
			}

			line := bytes.TrimSpace(buf.Bytes())
			if bytes.Contains(line, []byte(secret)) {
				t.Fatalf("core's JSON formatter disagrees with the masker on this builder, "+
					"so stderr still carries the secret; requires the corrected core dependency. line: %s", line)
			}
			if !bytes.Contains(line, []byte(redacted)) {
				t.Fatalf("console line carries neither the secret nor the redaction: %s", line)
			}
		})
	}
}

// Regex rules must reach the value too, not just field-name rules. Probed by
// behaviour rather than by inspecting the entry, and required to pass against
// the corrected core dependency.
func TestFanout_EveryExporterSeesRegexMaskedValues(t *testing.T) {
	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			var buf bytes.Buffer
			rec := &recorder{}
			b.emit(newMaskedLogger(&buf, rec), "contact", email)

			got := rec.only(t).attrs()["contact"].AsString()
			if got == email {
				t.Fatalf("core does not regex-mask values reaching the entry this way yet; "+
					"requires the corrected core dependency (attribute was %q)", got)
			}
			if got != emailMask {
				t.Fatalf("OTLP record exported %q, want %q", got, emailMask)
			}

			line := bytes.TrimSpace(buf.Bytes())
			if bytes.Contains(line, []byte(email)) {
				t.Fatalf("stderr still carries the address; requires the corrected core dependency. line: %s", line)
			}
		})
	}
}

// Masking rewrites the message in place; the record body must carry the
// rewrite, not the original.
func TestAdapter_MaskedMessageReachesBody(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	newMaskedLogger(&buf, rec).Info("card 4111 1111 1111 1111 declined")

	if body := rec.only(t).body(); body == "card 4111 1111 1111 1111 declined" {
		t.Fatalf("unmasked card number reached the record body: %q", body)
	}
}
