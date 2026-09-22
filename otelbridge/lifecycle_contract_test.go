package otelbridge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/log/global"
)

func TestLifecycle_LateGlobalProviderFlush(t *testing.T) {
	if os.Getenv("HALOLOG_REVIEW_GLOBAL_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycle_LateGlobalProviderFlush$", "-test.count=1")
		cmd.Env = append(os.Environ(), "HALOLOG_REVIEW_GLOBAL_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated global test: %v\n%s", err, out)
		}
		return
	}
	a := NewAdapter("review/late-global")
	p := &flushCountingProvider{rec: &recorder{}}
	paired := NewAdapter("review/late-global-paired", WithFlushFunc(p.ForceFlush))
	global.SetLoggerProvider(p)
	if err := a.Write(entryWith(0)); err != nil {
		t.Fatal(err)
	}
	_ = p.rec.only(t) // confirms the late provider received the record
	if err := a.Flush(); !errors.Is(err, ErrFlushUnavailable) {
		t.Fatalf("unknown global drain must be explicit, got %v", err)
	}
	if err := paired.Write(entryWith(0)); err != nil {
		t.Fatal(err)
	}
	if len(p.rec.all()) != 2 {
		t.Fatal("paired logger did not emit through the expected provider")
	}
	if err := paired.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := p.flushes.Load(); got != 1 {
		t.Fatalf("records reached late provider but Flush reached it %d times", got)
	}

	other := &flushCountingProvider{rec: &recorder{}}
	global.SetLoggerProvider(other)
	if err := paired.Flush(); err != nil {
		t.Fatal(err)
	}
	if p.flushes.Load() != 2 || other.flushes.Load() != 0 {
		t.Fatal("flush followed a replacement global instead of the emitting provider")
	}
}

type ownedLifecycleProvider struct {
	flushCountingProvider
	shutdowns atomic.Int64
}

func (p *ownedLifecycleProvider) Shutdown(context.Context) error {
	p.shutdowns.Add(1)
	return nil
}
func TestLifecycle_CloseDoesNotShutdownCallerProvider(t *testing.T) {
	p := &ownedLifecycleProvider{flushCountingProvider: flushCountingProvider{rec: &recorder{}}}
	a := NewAdapter("test/ownership", WithLoggerProvider(p))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if p.flushes.Load() != 1 || p.shutdowns.Load() != 0 {
		t.Fatal("Close did not preserve caller ownership")
	}
	if err := a.Write(entryWith(0)); err != nil {
		t.Fatal(err)
	}
	_ = p.rec.only(t)
}
func TestLifecycle_FlushCallbackDeadlineAndErrors(t *testing.T) {
	p := &flushCountingProvider{rec: &recorder{}}
	a := NewAdapter("test/callback", WithLoggerProvider(p), WithFlushTimeout(time.Millisecond),
		WithFlushFunc(func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("missing deadline")
			}
			<-ctx.Done()
			return ctx.Err()
		}))
	if err := a.Flush(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flush error: %v", err)
	}
	if p.flushes.Load() != 0 {
		t.Fatal("callback and provider were both drained")
	}
	sentinel := errors.New("drain failed")
	a = NewAdapter("test/no-deadline", WithLoggerProvider(p), WithFlushTimeout(0),
		WithFlushFunc(func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); ok {
				t.Error("unexpected deadline")
			}
			return sentinel
		}))
	if err := a.Flush(); !errors.Is(err, sentinel) {
		t.Fatalf("lost callback error: %v", err)
	}
}
