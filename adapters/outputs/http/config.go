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
// Package config provides configuration management
// Author: Admilson B. F. Cossa

package http

import (
	"net/http"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

// Transport defaults, shared by DefaultHTTPAdapterConfig and every adapter.
const (
	defaultMaxIdleConns    = 100
	defaultMaxConnsPerHost = 100
	defaultIdleConnTimeout = 90 * time.Second
)

// HTTPAdapterConfig configures an adapter built by NewHTTPAdapterFromConfig.
// Start from DefaultHTTPAdapterConfig and change what you need: a zero or
// negative number takes its default. The transport fields tune the
// adapter's own client; with Client set, that client is used as it is.
//
//nolint:revive // HTTP prefix is the intended public API name; renaming would break callers across the module.
type HTTPAdapterConfig struct {
	URL           string
	Method        string
	Headers       map[string]string
	BatchSize     int
	FlushInterval time.Duration
	Timeout       time.Duration // bounds each request, whichever client sends it
	Formatter     types.Formatter

	MaxIdleConns       int           // idle connections kept for reuse
	MaxConnsPerHost    int           // connections open to the endpoint at once
	IdleConnTimeout    time.Duration // how long an idle connection is kept
	DisableCompression bool          // do not ask the endpoint for gzip responses

	// Client sends the requests instead of the adapter's own, for TLS, mTLS,
	// or a client shared with the rest of the program. Close leaves it as it
	// is.
	Client *http.Client
}

// DefaultHTTPAdapterConfig returns the settings every adapter defaults to.
func DefaultHTTPAdapterConfig() *HTTPAdapterConfig {
	return &HTTPAdapterConfig{
		Method:             http.MethodPost,
		BatchSize:          defaultBatchSize,
		FlushInterval:      defaultFlushInterval,
		Timeout:            defaultTimeout,
		Headers:            map[string]string{"Content-Type": "application/json"},
		MaxIdleConns:       defaultMaxIdleConns,
		MaxConnsPerHost:    defaultMaxConnsPerHost,
		IdleConnTimeout:    defaultIdleConnTimeout,
		DisableCompression: true,
	}
}

// newTransport returns the adapter's own transport: the standard library's
// default one (proxy from the environment, dial and TLS handshake timeouts,
// HTTP/2) with the connection settings of cfg.
func newTransport(cfg *HTTPAdapterConfig) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = orDefault(cfg.MaxIdleConns, defaultMaxIdleConns)
	t.MaxIdleConnsPerHost = t.MaxIdleConns // every connection goes to one host
	t.MaxConnsPerHost = orDefault(cfg.MaxConnsPerHost, defaultMaxConnsPerHost)
	t.IdleConnTimeout = orDefault(cfg.IdleConnTimeout, defaultIdleConnTimeout)
	t.DisableCompression = cfg.DisableCompression
	return t
}
