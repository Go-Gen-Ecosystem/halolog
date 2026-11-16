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

package http_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	httpadapter "github.com/go-gen-ecosystem/halolog/adapters/outputs/http"
	"github.com/go-gen-ecosystem/halolog/core"
)

type batchEntry struct {
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Timestamp int64          `json:"timestamp"`
	Fields    map[string]any `json:"fields"`
}

// batchSink decodes every batch it receives.
func batchSink(t *testing.T) (url string, entries func() []batchEntry) {
	t.Helper()
	var mu sync.Mutex
	var got []batchEntry
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch struct {
			Timestamp int64        `json:"timestamp"`
			Entries   []batchEntry `json:"entries"`
		}
		if err := json.Unmarshal(body, &batch); err != nil || batch.Timestamp <= 0 {
			t.Errorf("invalid batch (%v): %s", err, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, batch.Entries...)
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []batchEntry {
		mu.Lock()
		defer mu.Unlock()
		return append([]batchEntry(nil), got...)
	}
}

// The logger recycles its entries as soon as an adapter's Write returns;
// batched lines must still arrive whole, with their fields and timestamps.
func TestHTTPAdapter_LoggerLinesSurviveBatching(t *testing.T) {
	logs := map[string]func(l *core.Logger, i int){
		"message": func(l *core.Logger, i int) { l.Info(fmt.Sprintf("line-%d", i)) },
		"WithField": func(l *core.Logger, i int) {
			l.WithField("i", i).Info(fmt.Sprintf("line-%d", i))
		},
		"Typed": func(l *core.Logger, i int) {
			l.Typed().WithInt("i", i).Info(fmt.Sprintf("line-%d", i))
		},
	}
	for name, log := range logs {
		t.Run(name, func(t *testing.T) {
			url, entries := batchSink(t)
			a := httpadapter.NewHTTPAdapterWithOptions(&httpadapter.HTTPAdapterOptions{URL: url, BatchSize: 1000})
			l := core.New().Adapter(a).MustBuild()
			const n = 50
			for i := range n {
				log(l, i)
			}
			if err := l.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			got := entries()
			if len(got) != n {
				t.Fatalf("received %d of %d lines", len(got), n)
			}
			for i, e := range got {
				if want := fmt.Sprintf("line-%d", i); e.Message != want || e.Level != "INFO" || e.Timestamp <= 0 {
					t.Fatalf("line %d = %+v, want message %q at INFO with a timestamp", i, e, want)
				}
				if name != "message" && e.Fields["i"] != float64(i) {
					t.Fatalf("line %d fields = %v, want i=%d", i, e.Fields, i)
				}
			}
		})
	}
}
