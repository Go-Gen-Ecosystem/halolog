// @author Admilson B. F. Cossa
package core

import (
	"errors"
	"runtime"
	"testing"

	"github.com/go-gen-ecosystem/halolog/pool"
	"github.com/go-gen-ecosystem/halolog/types"
)

// TestPerPPool_StaticFieldsNotShrunkAcrossReuse guards a pooling regression:
// a dispatch reslices the pooled entry's StaticFields to [:count]; if the pool
// does not restore the full backing buffer on the next borrow, a subsequent
// larger field chain silently drops fields beyond the previous count.
func TestPerPPool_StaticFieldsNotShrunkAcrossReuse(t *testing.T) {
	var got int
	adapter := &types.FuncAdapter{
		WriteFunc: func(e *types.LogEntry) error {
			got = e.StaticFieldCount
			return nil
		},
	}
	logger := New().Adapter(adapter).MustBuild()

	// First a two-field entry (reslices the pooled StaticFields to len 2).
	logger.WithField("a", 1).WithField("b", 2).Info("two")
	if got != 2 {
		t.Fatalf("two-field entry: StaticFieldCount = %d, want 2", got)
	}

	// Reusing the recycled state, a four-field entry must keep all four.
	logger.WithField("a", 1).WithField("b", 2).WithField("c", 3).WithField("d", 4).Info("four")
	if got != 4 {
		t.Fatalf("four-field entry after reuse: StaticFieldCount = %d, want 4 (fields were dropped)", got)
	}
}

// metadataProbe records the per-line metadata of every entry it receives.
type metadataProbe struct {
	file     string
	line     int
	err      error
	errorMsg string
	caller   any
	context  int
}

func (p *metadataProbe) Name() string                  { return "metadata-probe" }
func (p *metadataProbe) Write(e *types.LogEntry) error { return p.WriteZero(e) }
func (p *metadataProbe) WriteZero(e *types.LogEntry) error {
	p.file, p.line, p.err, p.errorMsg = e.File, e.Line, e.Error, e.ErrorMsg
	p.caller, p.context = e.Caller, len(e.Context)
	return nil
}
func (p *metadataProbe) Flush() error                 { return nil }
func (p *metadataProbe) Close() error                 { return nil }
func (p *metadataProbe) Health() error                { return nil }
func (p *metadataProbe) SetFormatter(types.Formatter) {}

// TestMessageOnlyLinesDoNotInheritPooledMetadata guards the shared entry pool
// contract on the message-only paths: an entry another user filled with a
// source location, an error, and context, then released, must reach the next
// line clean. GOMAXPROCS(1) keeps the same P, so the next borrow gets it back.
func TestMessageOnlyLinesDoNotInheritPooledMetadata(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	p := &metadataProbe{}
	l := NewLogger(Config{Level: types.TraceLevel, Adapters: []types.Adapter{p}})
	for _, level := range []struct {
		name string
		log  func(string)
	}{{"trace", l.Trace}, {"debug", l.Debug}, {"info", l.Info}, {"warn", l.Warn}, {"error", l.Error}} {
		e := pool.AcquireEntry()
		e.File, e.Line = "private-tenant-path.go", 417
		e.Error, e.ErrorMsg = errors.New("private-tenant-error"), "private-tenant-error"
		e.Caller = "private-tenant-caller"
		e.Context = []types.TypedFieldData{{Key: "tenant_secret", Value: "private-value"}}
		pool.ReleaseEntry(e)
		level.log("fresh event")
		if p.file != "" || p.line != 0 || p.err != nil || p.errorMsg != "" || p.caller != nil || p.context != 0 {
			t.Errorf("%s inherited metadata: file=%q line=%d err=%v msg=%q caller=%v context=%d",
				level.name, p.file, p.line, p.err, p.errorMsg, p.caller, p.context)
		}
	}
}
