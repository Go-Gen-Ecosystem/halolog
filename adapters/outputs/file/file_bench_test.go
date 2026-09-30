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

package file

import (
	"path/filepath"
	"testing"
)

// benchAdapter opens an adapter with the default configuration, without
// rotation, in a temporary directory.
func benchAdapter(b *testing.B) *FileAdapter {
	b.Helper()
	cfg := DefaultRotationConfig()
	cfg.MaxSize = 0
	adapter, err := NewFileAdapter(filepath.Join(b.TempDir(), "bench.log"), cfg)
	if err != nil {
		b.Fatalf("NewFileAdapter: %v", err)
	}
	b.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

// reportDropped records the share of lines the adapter dropped instead of
// queuing, so a queue that drops under load cannot pass for a faster one.
func reportDropped(b *testing.B, adapter *FileAdapter) {
	b.ReportMetric(float64(adapter.Metrics().LostWrites.Load())/float64(b.N), "dropped/op")
}

// BenchmarkFileAdapter_Write measures one goroutine queuing lines; the writer
// goroutine writes them to the file concurrently.
func BenchmarkFileAdapter_Write(b *testing.B) {
	adapter := benchAdapter(b)
	entry := newTestEntry("benchmark line with a few words of payload")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = adapter.Write(entry)
	}
	reportDropped(b, adapter)
}

// BenchmarkFileAdapter_WriteParallel measures GOMAXPROCS goroutines queuing
// lines into one adapter.
func BenchmarkFileAdapter_WriteParallel(b *testing.B) {
	adapter := benchAdapter(b)
	entry := newTestEntry("benchmark line with a few words of payload")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = adapter.Write(entry)
		}
	})
	reportDropped(b, adapter)
}
