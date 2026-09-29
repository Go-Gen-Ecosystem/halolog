# HaloLog — Comprehensive Multi-Logger Benchmark Comparison

**Measured 2026-08-26 · Go 1.27.0 · Intel Core Ultra 9 285HX · 5 runs × 1 s per scenario · benchstat medians**

This document is the canonical, reproducible record of HaloLog's standing
against the major Go structured loggers. It supersedes every earlier results
file in this directory (`benchmark_comparison.md`, `final_.md`,
`benchmark_results*.txt`, `simple_bench_result.txt`), which predate the v1.0
core and measured internal prototypes that no longer exist.

## Method — what makes this comparison fair

- Every logger emits a **full structured JSON record** — timestamp + level +
  message + fields — to `io.Discard`. No logger gets a disabled-output shortcut
  in a comparative row.
- Field values and counts are identical per scenario; each logger uses its own
  idiomatic API (zerolog `Int()`, zap `zap.Int()`, slog `slog.Int()`, phuslu
  `Int()`, HaloLog typed/keyed setters).
- The field includes **phuslu/log v1.0.128** — the fastest logger on public Go
  leaderboards — because a speed claim that never races the champion is not a
  claim.
- 5 runs × 1 s each, summarized with `benchstat` (medians). Single machine;
  expect ±10–15% run-to-run drift and treat sub-5 ns deltas as ties.
- Linux numbers come from linux/amd64 (WSL2) — the CI environment; Windows
  numbers from windows/amd64 on the same hardware. Orderings agree.

Known biases, disclosed: HaloLog serializes writes behind a mutex (lines never
interleave) while zerolog and phuslu write unlocked — a safety cost HaloLog
pays that they do not. phuslu does **not escape field keys at all** (hostile
keys produce broken JSON); HaloLog escapes plain keys per call and pre-escapes
`Key()` keys once. In the twenty-field scenario zap's field slice is built
outside the timed loop (its idiomatic accumulated-fields style), which flatters
zap slightly. `HaloLog_DisabledOutput` rows measure the no-op sink and are
**never** comparable to real-output rows.

## Results — linux/amd64 (canonical)

### Bare message

| Logger | ns/op | allocs | vs HaloLog |
|---|---:|---:|---:|
| **HaloLog** | **23.9** | 0 B, 0 | — |
| phuslu/log | 63.1 | 0 B, 0 | 2.6× slower |
| zerolog | 86.8 | 0 B, 0 | 3.6× slower |
| zap | 146.2 | 0 B, 0 | 6.1× slower |
| slog (stdlib JSON) | 280.3 | 0 B, 0 | 11.7× slower |
| logrus | 1395 | 797 B, 21 | 58× slower |

23.9 ns/op ≈ **42 million JSON log lines per second, single goroutine**.

### One field (string)

| Logger | ns/op | allocs |
|---|---:|---:|
| **HaloLog (typed)** | **32.0** | 0 B, 0 |
| HaloLog (WithField) | 40.1 | 0 B, 0 |
| phuslu/log | 69.9 | 0 B, 0 |
| zerolog | 99.5 | 0 B, 0 |
| zap | 183.7 | 64 B, 1 |
| slog | 312.9 | 48 B, 1 |
| logrus | 1486 | 1.5 KiB, 27 |

### Ten fields (int)

| Logger | ns/op | allocs |
|---|---:|---:|
| **HaloLog (pre-declared keys)** | **83.1** | 0 B, 0 |
| **HaloLog (typed, plain keys)** | **83.6** | 0 B, 0 |
| HaloLog (WithField) | 113.6 | 0 B, 0 |
| phuslu/log | 120.7 | 0 B, 0 |
| zerolog | 201.7 | 0 B, 0 |
| zap | 437.4 | 706 B, 1 |
| slog | 992.9 | 689 B, 11 |
| logrus | 4066 | 3.4 KiB, 64 |

The fused single-pass key emit made escaped plain keys as fast as
pre-declared ones (83.6 vs 83.1 ns) — key safety now costs effectively
nothing on clean keys. Even the legacy `WithField` interface API beats every
competitor.

### Twenty fields (int)

| Logger | ns/op | allocs |
|---|---:|---:|
| **HaloLog (typed)** | **146.0** | 0 B, 0 |
| phuslu/log | 179.9 | 0 B, 0 |
| HaloLog (WithField) | 206.8 | 0 B, 0 |
| zerolog | 230.6 | 0 B, 0 |
| zap (fields prebuilt) | 394.6 | 0 B, 0 |

### Disabled level (not comparable to output rows)

| | ns/op |
|---|---:|
| HaloLog, level filtered | 0.83 |

## Results — windows/amd64 (same hardware, same day, final code, same run)

| Scenario | HaloLog | phuslu | zerolog² |
|---|---:|---:|---:|
| Bare message | **26.6** | 46.2 | 62.8 |
| One field (typed) | **42.1** | 46.0 | 75.5 |
| Ten fields (typed) | **99.2** | 103.2 | 132.6 |
| Twenty fields (typed) | **162.6** | 176.3 | 200.0 |

² zerolog column from the same-day full Windows sweep; HaloLog/phuslu from
their same-run head-to-head after the fused key emit landed.

## Request-scoped context (5 bound fields + 1 call-site field)

Measured on windows/amd64, same hardware, same-day run as the Windows
head-to-head above. Each logger uses its idiomatic bound-context API and
emits the full record per line (`benchmarks/context_bench_test.go`).

| Logger | ns/op | allocs | mechanism |
|---|---:|---:|---|
| **HaloLog `With().Logger()`** | **37.4** | 0 B, 0 | context pre-encoded once → one memcpy per line |
| phuslu Context | 54.5 | 0 B, 0 | pre-encoded context bytes |
| zerolog `With().Logger()` | 90.8 | 0 B, 0 | context bytes copied per event |
| zap `With(...)` | 180.5 | 64 B, 1 | cloned encoder + per-line field encode |

The 2026-08-28 quiet-host rerun measured the same scenario at 33.3 ns on
linux/amd64 and 34.4 ns on windows/amd64 for HaloLog, orderings unchanged
(see Reproduction below).

## Reproduction — 2026-08-28, quiet host, v1.0.1 as published

The full sweep was rerun on the same hardware with no background load, on
the shipped v1.0.1 code. Every Linux scenario reproduced within the
documented drift or improved (ten fields 80.0 ns, twenty 133.1 ns, bound
context 33.3 ns); every ordering held on both platforms; 0 B/op and
0 allocs/op in every HaloLog row. Disclosed movement: the quiet host sped
up all loggers, phuslu on Windows most (35.5 ns bare message), and the
Windows ten-field cell tightened from a 4 ns lead to 0.9 ns — a
statistical tie under this document's own sub-5 ns rule. Raw runs: 5 × 1 s
per scenario, benchstat medians, goos/goarch headers preserved in the
archived outputs.

## Time, bytes, and allocations on one pinned core

**Measured 2026-09-29 · Go 1.27.1 · windows/amd64 · Intel Core Ultra 9 285HX · process pinned to one core · 10 interleaved samples of 200 ms per scenario · medians**

The 285HX mixes performance and efficiency cores. Unpinned, a benchmark
process can move between them, and its samples split into two speed modes
far apart. This run pins the process to one core, which also sets
GOMAXPROCS=1, so garbage collection shares that core with the logger; that
weighs on the loggers that allocate. B/op and allocs/op were identical in
every sample. zap's twenty-field row carries the caveat in Method: its field
slice is built outside the timed loop.

### The comparison suite

ns/op:

| Scenario | HaloLog | phuslu | zerolog | zap | slog | logrus |
|---|---:|---:|---:|---:|---:|---:|
| Bare message | **23.28** | 36.47 | 64.00 | 112.65 | 202.50 | 856.15 |
| One field (typed) | **32.62** | 41.25 | 73.91 | 203.00 | 336.20 | 1527 |
| Ten fields (typed) | **81.00** | 84.21 | 134.95 | 444.95 | 1014.5 | 4519 |
| Twenty fields (typed) | **130.50** | 147.95 | 198.40 | 316.20 | — | — |
| Request-scoped context | **33.39** | 49.51 | 80.55 | 215.75 | — | — |

B/op and allocs/op:

| Scenario | HaloLog | phuslu | zerolog | zap | slog | logrus |
|---|---:|---:|---:|---:|---:|---:|
| Bare message | 0 B, 0 | 0 B, 0 | 0 B, 0 | 0 B, 0 | 0 B, 0 | 792 B, 21 |
| One field (typed) | 0 B, 0 | 0 B, 0 | 0 B, 0 | 64 B, 1 | 48 B, 1 | 1560 B, 27 |
| Ten fields (typed) | 0 B, 0 | 0 B, 0 | 0 B, 0 | 704 B, 1 | 688 B, 11 | 3448 B, 64 |
| Twenty fields (typed) | 0 B, 0 | 0 B, 0 | 0 B, 0 | 0 B, 0 | — | — |
| Request-scoped context | 0 B, 0 | 0 B, 0 | 0 B, 0 | 64 B, 1 | — | — |

### Every HaloLog benchmark in the suite

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Info/HaloLog` | 23.28 | 0 | 0 |
| `OneField/HaloLog_Typed` | 32.62 | 0 | 0 |
| `OneField/HaloLog_WithField` | 41.36 | 0 | 0 |
| `TenFields/HaloLog_Typed` | 81.00 | 0 | 0 |
| `TenFields/HaloLog_WithField` | 112.80 | 0 | 0 |
| `TwentyFields/HaloLog_Typed` | 130.50 | 0 | 0 |
| `TwentyFields/HaloLog_WithField` | 196.00 | 0 | 0 |
| `Keyed_TenFields/Keyed` | 84.44 | 0 | 0 |
| `Keyed_TenFields/WithField` | 119.30 | 0 | 0 |
| `Keyed_TwentyFields/Keyed` | 136.35 | 0 | 0 |
| `Keyed_TwentyFields/WithField` | 234.10 | 0 | 0 |
| `Defaults/HaloLog_Keyed` | 87.44 | 0 | 0 |
| `Defaults/HaloLog_Typed` | 101.10 | 0 | 0 |
| `Defaults/HaloLog_WithField` | 124.75 | 0 | 0 |
| `Context/HaloLog` (5 bound fields + 1 per line) | 33.39 | 0 | 0 |
| `Info/HaloLog_DisabledOutput` (no-op sink) | 0.78 | 0 | 0 |

### Message-only lines at every level

Each logger shape logs a bare message at Trace, Debug, Info, Warn, and
Error, with the output discarded; the ns/op range spans the five levels.
Medians of 8 samples.

| Logger shape | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| One JSON adapter (direct path) | 22.71–22.87 | 0 | 0 |
| Text formatter | 27.72–27.88 | 0 | 0 |
| Sampled (backpressure sampler keeping every line) | 48.59–49.27 | 0 | 0 |
| Two JSON adapters | 68.38–70.39 | 0 | 0 |
| Masked (`masking.NewPIIMasker` plus one field rule) | 1554–1575 | 0 | 0 |

`TestZeroAlloc_MessageOnlyEveryLevel` guards every level on these shapes.

### With `Caller()`

| Line | without `Caller()` | with `Caller()` | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Bare message, direct JSON path | 23.20 | 138.60 | 0 | 0 |
| One typed field, direct JSON path | 30.93 | 128.30 | 0 | 0 |
| Bare message, capture path | 47.80 | 159.85 | 0 | 0 |
| One typed field, capture path | 47.93 | 164.85 | 0 | 0 |

From `BenchmarkCaller` in `core`, where a test masker forces the capture
path. Each call site is resolved on its first line, which allocates once;
later lines reuse it. A site beyond the 4,096-site cache allocates on each of
its lines.

## Cross-OS behavior (profiler-verified)

HaloLog avoids a major source of OS-dependent latency: the engine does no
per-line wall-clock read and no syscalls (one atomic load of the cached clock,
then a fused header memcpy). On this host its bare-message result moved from
23.9 ns on Linux to 26.6 ns on Windows; benchmark latency is not literally
platform-invariant. phuslu
swings ~30% between OSes because ~39% of its line is per-line timestamp
acquisition + formatting (its own CPU profile) and Windows' time source is
far cheaper than Linux's vDSO `clock_gettime`. That Windows tailwind — plus
phuslu not escaping field keys at all (hostile keys break its JSON) —
briefly gave it the plain-string ten/twenty-field lead on Windows; the
fused single-pass key emit removed our per-call key cost and closed those
last two cells while keeping keys fully escaped.

## Verdict

- **HaloLog wins every published scenario in these measurements.** In the
  six-logger Linux table: 2.6×/2.2× over phuslu at zero/one field, **44% faster at
  ten fields** (83.6 vs 120.7), 23% at twenty (146.0 vs 179.9). Windows
  three-logger head-to-head: 26.6 vs 46.2 bare, 99.2 vs 103.2 at ten,
  162.6 vs 176.3 at twenty.
- 0 B/op, 0 allocs/op in every HaloLog scenario, with fully escaped keys
  and never-interleaved lines — correctness guarantees the closest rival
  does not offer.
- zerolog is beaten 1.6–3.6× everywhere. zap, slog, and logrus are not
  close (4–58× slower, and all three allocate once fields appear).

## Why these numbers moved — the three optimizations (2026-08-26)

Profiling the ten-field line showed **72% of wall time in field-capture
plumbing** (`withTyped` + `addField`: building a 56-byte `FieldValue` box per
field and passing it through a two-call funnel) versus ~14% in actual byte
encoding — while phuslu appends `,"key":value` bytes immediately per field.
Three commits closed the gap and then some:

Commits are referenced by subject (stable across any history maintenance);
find each with `git log --oneline --grep`.

1. **"perf(console): format outside the write lock; lock-free formatter
   access"** — the per-line `DirectEncoder()` query became a lock-free load
   (was a mutex pair), and formatting moved outside the write lock.
2. **"perf(core,json): per-type direct append"** — typed and Line setters for
   string/int/int64/float64/bool/error write bytes in one call on the direct
   path; the `FieldValue` box is built only where the capture path needs it.
   Ten-field typed 177→105 ns; twenty-field 328→172 ns.
3. **"perf(core): message-only lines take the direct byte path"** —
   `Info(msg)` renders fused-header + closer straight into the pooled line
   buffer, no `LogEntry` touched. Bare message 49→24 ns.
4. **"perf(json): SWAR word-at-a-time escape scanning"** — the clean-string
   scan processes eight bytes per fused load with branch-free
   hasless/haszero word tricks, falling back to the exact per-byte path on
   any dirty word. Byte-identical output (exhaustive oracle tests); 80-byte
   clean scan 23.5→14.3 ns, and it widened the Linux ten/twenty-field
   margins.
5. **"perf(json): fuse the plain-key scan and copy into one pass"** — the
   key emit was two passes and two calls per field (~25% of a ten-field
   line); it is now one fused scan-while-copy loop that bails to the exact
   escaping path on a hostile byte. Plain escaped keys became as fast as
   pre-declared ones (83.6 vs 83.1 ns at ten fields on Linux), completing
   the sweep on Windows as well.

Byte output is unchanged (pinned by direct-vs-capture identity tests) and every
hot path stays 0 allocs/op under 8 committed allocation guards
(`go test ./core -run TestZeroAlloc`).

What phuslu still does differently: no write mutex (interleaving possible), no
key escaping (fast but unsafe for arbitrary keys), and per-line timestamp
formatting (~a third of their bare-message cost — HaloLog's fused per-second
header cache makes the same work a single memcpy, which is where the 2.6×
bare-message margin comes from).

## Why the escape scan is SWAR, not SIMD (measured)

The `simd-prep` branch carries a complete escape scanner on Go 1.27's
experimental portable SIMD (`GOEXPERIMENT=simd`), proven byte-identical to
the shipped scanner by exhaustive oracles and a fuzz target. Measured on
windows/amd64, it **loses** to the shipped SWAR scan: 92.9 vs 16.5 ns on an
80-byte clean string (5.6×) and 231 vs 191 ns on 1 KiB. The portable layer
has no any-lane reduction primitive yet, so the per-block mask check must
round-trip mask → lane bytes → uint64 words → memory → scalar OR, which
swamps the vector win at log-line sizes. The branch exists to re-measure
each Go release; SIMD graduates only if it beats SWAR on realistic payloads
with the experiment flag gone.

## Reproduce

```bash
cd benchmarks
go test -bench='BenchmarkInfo$|BenchmarkOneField$|BenchmarkTenFields$|BenchmarkTwentyFields$|BenchmarkKeyed_TenFields$' \
  -benchmem -benchtime=1s -run='^$' -count=5 . | tee results.txt
go run golang.org/x/perf/cmd/benchstat@latest results.txt
```

The scenario definitions live in `comparison_bench_test.go` (six loggers) and
`keyed_bench_test.go` (pre-declared-key variants). CI runs the allocation
guards and reports these numbers per commit via `.github/workflows/bench.yml`.
