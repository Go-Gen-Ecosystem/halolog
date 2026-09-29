# Performance Guide

HaloLog's performance claims are measured, committed, and reproducible — never
aspirational. The canonical numbers, method, and fairness notes live in
[`benchmarks/comprehensive_comparison.md`](../benchmarks/comprehensive_comparison.md);
as of 2026-09-29 (Go 1.27.1, linux/amd64, medians of 10 runs) HaloLog leads a
six-logger field — phuslu/log, zerolog, zap, slog, logrus — in every scenario:
**22.6 ns bare message, 33.7 ns one field, 79.4 ns ten fields (80.0 ns with
pre-declared keys), 0.58 ns disabled level, 0 B/op and 0 allocs/op throughout.**

Ten committed allocation guards pin the hot paths at zero allocations:

```bash
go test ./core -run TestZeroAlloc
```

## How the hot path works

- **Fused header cache** — the entire `{"time":"…","level":"…"` prefix is
  cached per (second, level) behind lock-free atomic pointers; on the steady
  state the header is a single memcpy. Sub-second precisions cache the
  per-second RFC3339 prefix and render only fractional digits.
- **Direct byte path** — with a single raw-capable JSON adapter and no
  masking/sampling, message-only calls and every typed/Line field append
  encode straight to bytes in the pooled line buffer (no LogEntry, no
  intermediate value boxes). Output is byte-identical to the capture path
  (test-pinned), and any other configuration falls back automatically — so
  PII masking can never be bypassed by the fast path.
- **Per-P pooled state** — builders borrow a pooled per-processor state; an
  epoch counter makes accidental reuse after a terminal call a free no-op
  instead of undefined behavior.
- **Startup-time dispatch specialization** — each level's function pointer is
  selected once at construction (direct / discard / masking × adapter-count /
  sampled), so the per-call path carries no configuration branching.

## Getting the fastest configuration

```go
// Direct-path eligible: exactly one JSON console/raw adapter,
// no masking, no sampling.
log := core.New().
    Level(types.InfoLevel).
    Adapter(console.NewWithWriter(os.Stdout, json.NewJsonFormatter())).
    MustBuild()

log.Info("started")                          // ~24 ns, 0 allocs
log.Typed().WithInt("status", 200).Info("ok") // typed fields, 0 allocs
```

### Pre-declare keys on the hottest lines

A `halolog.Key` escapes its JSON fragment once at declaration; every use is a
single memcpy — measurably faster than per-call key escaping and immune to
hostile key bytes:

```go
var (
    keyUser   = halolog.Key("user_id")
    keyStatus = halolog.Key("status")
)

log.Typed().Str(keyUser, "alice").Int(keyStatus, 200).Info("handled")
```

### Level-first lines make disabled logging free

```go
log.DebugLine().Str(keyUser, "alice").Msg("verbose") // ~0.8 ns when DEBUG is off
```

`DebugLine()` on a filtered level returns a no-op line before any field work
happens.

## What disables the direct path (by design)

Multiple adapters, PII masking, sampling, or a non-JSON formatter switch the
logger to the capture path — still allocation-free through the 64-slot static
field buffer, byte-identical output, just without the direct-append shortcut.
Masking and sampling are transforms that must see every entry; the eligibility
rule exists so no optimization can skip them.

## Measuring yourself

```bash
cd benchmarks
go test -bench='BenchmarkInfo$|BenchmarkOneField$|BenchmarkTenFields$' \
  -benchmem -benchtime=1s -run='^$' -count=5 .
```

Compare runs with `benchstat`. Numbers vary by machine; orderings have been
stable across windows/amd64 and linux/amd64. When you change anything on the
hot path, the allocation guards plus `benchmarks/` are the regression net.
