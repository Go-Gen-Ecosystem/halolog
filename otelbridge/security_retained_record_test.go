package otelbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/go-gen-ecosystem/halolog/types"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	"go.opentelemetry.io/otel/trace"
)

// Unlike a helpful SDK, this consumer does not Clone or serialize at Emit.
// The adapter must transfer a record that remains valid after its caller reuses
// the entry and payload. A retaining consumer must never observe that reuse.
type securityRetainer struct {
	embedded.Logger
	mu       sync.Mutex
	contexts []context.Context
	records  []otellog.Record
}

func (*securityRetainer) Enabled(context.Context, otellog.EnabledParameters) bool { return true }
func (r *securityRetainer) Emit(ctx context.Context, record otellog.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contexts = append(r.contexts, ctx)
	r.records = append(r.records, record) // deliberately no Clone
}

func TestSecurityRetainedRecordsOwnStorage(t *testing.T) {
	for _, width := range []int{1, 5, 6, 16, 17, 64} {
		t.Run(fmt.Sprintf("attributes%d", width), func(t *testing.T) {
			sink := &securityRetainer{}
			adapter := &Adapter{logger: sink}
			var wg sync.WaitGroup
			for worker := range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, entry := requestFixture(uint64(worker + 1))
					correlation := append([]types.TypedFieldData(nil), entry.Fields[2:]...)
					fields := make([]types.TypedFieldData, width+len(correlation))
					payload := []byte("ORIGINAL")
					for iteration := range 32 {
						body := fmt.Sprintf("worker%d/record%d", worker, iteration)
						copy(payload, "ORIGINAL")
						fields[0] = types.TypedFieldData{Key: "payload", Value: payload}
						for j := 1; j < width; j++ {
							fields[j] = types.TypedFieldData{Key: fmt.Sprintf("field%d", j), Val: types.StringValue(body)}
						}
						copy(fields[width:], correlation)
						entry.Fields, entry.Message = fields, body
						if err := adapter.Write(entry); err != nil {
							t.Error(err)
							return
						}
						copy(payload, "MUTATED!")
						clear(fields)
						entry.Message = "recycled"
					}
				}()
			}
			wg.Wait()
			runtime.GC()
			if len(sink.records) != 128 {
				t.Fatalf("retained %d records, want 128", len(sink.records))
			}
			seen := make(map[string]bool)
			for i, record := range sink.records {
				body := record.Body().AsString()
				var worker, iteration int
				if n, err := fmt.Sscanf(body, "worker%d/record%d", &worker, &iteration); n != 2 || err != nil || worker < 0 || worker >= 4 || iteration < 0 || iteration >= 32 || seen[body] {
					t.Fatalf("record identity changed or duplicated: %q", body)
				}
				seen[body] = true
				wantCtx, _ := requestFixture(uint64(worker + 1))
				if !trace.SpanContextFromContext(sink.contexts[i]).Equal(trace.SpanContextFromContext(wantCtx)) {
					t.Fatal("cross-request context leak")
				}
				if record.AttributesLen() != width {
					t.Fatal("retained attribute count changed")
				}
				count := 0
				record.WalkAttributes(func(kv otellog.KeyValue) bool {
					wantKey := fmt.Sprintf("field%d", count)
					if count == 0 {
						if kv.Key != "payload" || string(kv.Value.AsBytes()) != "ORIGINAL" {
							t.Error("retained payload aliases caller memory")
						}
					} else if kv.Key != wantKey || kv.Value.AsString() != body {
						t.Error("retained attributes alias reused staging")
					}
					count++
					return true
				})
			}
		})
	}
}

func requestFixture(id uint64) (context.Context, *types.LogEntry) {
	var tid trace.TraceID
	var sid trace.SpanID
	binary.BigEndian.PutUint64(tid[8:], id)
	binary.BigEndian.PutUint64(sid[:], id)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	e := entryWith(2)
	e.Fields = append(e.Fields,
		types.TypedFieldData{Key: keyTraceID.Name, Val: types.StringValue(tid.String())},
		types.TypedFieldData{Key: keySpanID.Name, Val: types.StringValue(sid.String())},
		types.TypedFieldData{Key: keyTraceFlags.Name, Val: types.StringValue("01")})
	return trace.ContextWithSpanContext(context.Background(), sc), e
}
