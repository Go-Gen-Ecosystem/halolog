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

package http

import (
	"net/http"
	"testing"
	"time"
)

func transportOf(t *testing.T, a *HTTPAdapter) *http.Transport {
	t.Helper()
	tr, ok := a.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the adapter's own client has transport %T", a.client.Transport)
	}
	return tr
}

// Every field of the config reaches the adapter and its transport.
func TestNewHTTPAdapterFromConfig_AppliesEveryField(t *testing.T) {
	cfg := &HTTPAdapterConfig{
		URL:                "http://127.0.0.1:1/ingest",
		Method:             http.MethodPut,
		Headers:            map[string]string{"X-Key": "v"},
		BatchSize:          7,
		FlushInterval:      time.Minute,
		Timeout:            3 * time.Second,
		Formatter:          &mockFormatter{},
		MaxIdleConns:       4,
		MaxConnsPerHost:    2,
		IdleConnTimeout:    time.Second,
		DisableCompression: true,
	}
	a := NewHTTPAdapterFromConfig(cfg)
	defer func() { _ = a.Close() }()

	if a.url != cfg.URL || a.method != http.MethodPut || a.header.Get("X-Key") != "v" ||
		a.batchSize != 7 || a.flushInterval != time.Minute || a.timeout != 3*time.Second ||
		a.client.Timeout != 3*time.Second {
		t.Fatalf("adapter settings: url %q method %s header %v batch %d interval %v timeout %v/%v",
			a.url, a.method, a.header, a.batchSize, a.flushInterval, a.timeout, a.client.Timeout)
	}
	if _, ok := (*a.formatter.Load()).(*mockFormatter); !ok {
		t.Fatal("the configured formatter is not used")
	}
	tr := transportOf(t, a)
	if tr.MaxIdleConns != 4 || tr.MaxIdleConnsPerHost != 4 || tr.MaxConnsPerHost != 2 ||
		tr.IdleConnTimeout != time.Second || !tr.DisableCompression {
		t.Fatalf("transport: idle %d/%d, per host %d, idle timeout %v, no compression %v",
			tr.MaxIdleConns, tr.MaxIdleConnsPerHost, tr.MaxConnsPerHost, tr.IdleConnTimeout, tr.DisableCompression)
	}
	if tr.Proxy == nil || tr.TLSHandshakeTimeout == 0 {
		t.Fatal("the transport lost the standard library's proxy or TLS handshake settings")
	}
}

// DefaultHTTPAdapterConfig holds exactly what an adapter defaults to, so a
// config built from it, a nil config, an empty one, and the options
// constructor all agree.
func TestDefaultHTTPAdapterConfig_MatchesTheDefaults(t *testing.T) {
	for name, a := range map[string]*HTTPAdapter{
		"DefaultHTTPAdapterConfig": NewHTTPAdapterFromConfig(DefaultHTTPAdapterConfig()),
		"nil config":               NewHTTPAdapterFromConfig(nil),
		"empty config":             NewHTTPAdapterFromConfig(&HTTPAdapterConfig{}),
		"options":                  NewHTTPAdapterWithOptions(&HTTPAdapterOptions{}),
	} {
		tr := transportOf(t, a)
		if a.method != http.MethodPost || a.batchSize != defaultBatchSize ||
			a.flushInterval != defaultFlushInterval || a.timeout != defaultTimeout ||
			a.header.Get("Content-Type") != "application/json" ||
			tr.MaxIdleConns != defaultMaxIdleConns || tr.MaxConnsPerHost != defaultMaxConnsPerHost ||
			tr.IdleConnTimeout != defaultIdleConnTimeout {
			t.Errorf("%s: method %s batch %d interval %v timeout %v header %v transport %d/%d/%v",
				name, a.method, a.batchSize, a.flushInterval, a.timeout, a.header,
				tr.MaxIdleConns, tr.MaxConnsPerHost, tr.IdleConnTimeout)
		}
		_ = a.Close()
	}
}

// With Client set, the config's client sends the requests and Close leaves
// its connections alone.
func TestNewHTTPAdapterFromConfig_UsesTheConfiguredClient(t *testing.T) {
	transport := &countingTransport{}
	c := newCollector(t)
	cfg := DefaultHTTPAdapterConfig()
	cfg.URL, cfg.Client = c.URL, &http.Client{Transport: transport}

	a := NewHTTPAdapterFromConfig(cfg)
	_ = a.Write(line(0))
	if err := a.Flush(); err != nil || transport.requests.Load() != 1 {
		t.Fatalf("Flush = %v after %d requests through the configured client", err, transport.requests.Load())
	}
	if a.ownsClient {
		t.Fatal("the adapter claims a client it was given")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := c.delivered(t); got != 1 {
		t.Fatalf("delivered %d lines, want 1", got)
	}
}
