<!--
@author Admilson B. F. Cossa
-->
# Changelog

All notable changes to HaloLog are documented here. This project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **`otelbridge.NewAdapter` — logs out to OpenTelemetry.** An output adapter
  emitting each entry into the OpenTelemetry Logs API, so a line can reach an
  OTLP backend as a LogRecord while a console or file adapter keeps writing it
  to stderr; register both and the logger's existing fan-out does the rest.
  `Bind`'s `trace_id`/`span_id` fields are parsed back into a span context so
  the record carries a real TraceID and SpanID — what a backend correlates on
  — rather than three attributes it cannot join against. Fields, context,
  component, error, and caller location become attributes; HaloLog levels map
  onto the OpenTelemetry severity scale, and record timestamps resolve
  `TimestampUnix` before the wall-clock `Timestamp`, matching the JSON
  formatter — the hot path writes only the former, so the other order dates
  every record to the zero time. Masked values win over the typed original a
  masker replaced, so nothing the masker caught crosses the export boundary.
  Unsigned values above `math.MaxInt64` emit as exact decimal strings rather
  than wrapping negative, and `[]byte` attributes are copied, because
  `log.BytesValue` retains the caller's array while records outlive the call.
  Correlation is consumed per field: a valid trace id is the only requirement
  (the data model allows a record that names its trace but no span), and a
  field that fails to parse is left in the attributes rather than dropped.
  A logged field wins over metadata the adapter would derive under the same
  key, so `component`, `error`, and the source-location keys never appear
  twice on one record; fields with no key are dropped rather than emitted
  under `""`. A `LoggerProvider` handing back a nil `Logger` is reported
  through `Write` and `Health` instead of panicking inside the logging call.
  Per-shape allocation budgets are measured and gated by
  `TestAdapter_AllocationBudgets` (0 allocs up to five attributes; 2 for a
  correlated record's span context). A severity the SDK drops skips the
  attribute work but still pays for the span context, because `Enabled` is
  asked under the same correlation context `Emit` uses. `Flush` and
  `Close` drain the provider through `ForceFlush`, bounded by
  `WithFlushTimeout` (`DefaultFlushTimeout`, 5s, otherwise) — `Fatal` flushes
  and exits from inside the logging call, so a no-op flush would lose the last
  line a program writes and an unbounded one would hold the process open —
  but never `Shutdown` it, which stays the caller's. End-to-end tests run the
  real `sdk/log` pipeline and assert what an exporter receives. The adapter
  owns no shutdown lifecycle: `Flush` and `Close` request `ForceFlush`, while
  provider shutdown remains the caller's responsibility. Adds
  `go.opentelemetry.io/otel/log` to the `otelbridge` module only; the core
  logger's dependencies are unchanged.

### Corrected dependency and validation
- Require HaloLog core v1.0.2, making field-name and regex masking contracts
  unconditional across console and OpenTelemetry output.
- Make final correlation occurrences authoritative so a masked, malformed, or
  wrongly typed duplicate cannot restore an earlier trace identity.
- Bound attribute staging by record shape: 9 and 16 scalar fields now require
  one allocation; retained-record tests protect value ownership.
- Add published/local dependency gates on Go 1.24 and stable, real-SDK
  context-sensitive filtering, negative controls, lifecycle/backpressure tests,
  and separate uninstrumented allocation contracts.

## [1.0.2] - 2026-09-16

### Fixed
- Preserve masking across typed, legacy, static, bound, indexed and descriptor
  field representations. Reject invalid policy updates without changing the
  active policy and preserve redaction across overlapping regex matches.
- Remove named masking rules by registration ownership rather than regex text;
  replacing a name no longer accumulates stale rules or removes unrelated policy.
- Remove process-lifetime retention of arbitrary regex inputs/results.
- Synchronize encryption-prefix reads and preserve independently owned results.

### Performance and verification
- Reuse exclusive, scrubbed encryption scratch while retaining AES-GCM wire
  compatibility and fresh random nonces. Selected warmed small-field operations
  require one allocation; this is not a universal allocation-free claim.
- Add regression, independent-oracle, fuzz, retention and concurrency tests.
  Enforce allocation budgets separately from race/coverage instrumentation.
- Document the security and measurement boundaries in
  [the release validation notes](docs/releases/v1.0.2.md).

## [1.0.1] - 2026-08-28

First public release. (A `v1.0.0` tag was cut minutes earlier and retracted
the same day after a repository cleanup removed the parked prototype archive
from the published tree and history; depend on `v1.0.1` or later.) Everything
below is new since the internal 1.0.0 milestone.

### Changed (publication cleanup)
- The parked prototype archive is no longer part of the published repository
  or its history; superseded prototypes are kept in a local archive instead.
- Added `CITATION.cff` and a README citation entry.

### Fixed (hardening)
- **Linux/macOS build restored** — `syslog_unix.go` referenced a package-local
  formatter deleted in an earlier refactor and had never compiled since; it now
  uses `types.Formatter` with the text formatter as default. Stale
  `amd64 || arm64` build tags on the (pure-Go) JSON/text formatters and a wrong
  package clause in the file-lock fallback are gone: the module now builds on
  windows, linux, darwin, 386, riscv64, and wasm.
- **Level filtering on the classic fluent API** — `WithField(...).Info/Debug/…`
  bypassed the level filter entirely; every FieldBuilder terminal now dispatches
  through the same `dispatchLine` as the typed and Line APIs, which enforces
  level, sampling, masking, and metrics identically everywhere.
- **Sampling is real** — `Builder.Sampling`/`Config.Sampler` stored the sampler
  but never consulted it. `ShouldSample` now gates every Trace–Error line on
  all APIs (message-only, FieldBuilder, Typed, Line); Fatal/Panic are never
  sampled away.
- **Pooled-state corruption from builder reuse** — finishing a builder twice
  double-released its pooled state, later handing one state to two live
  builders (silent wrong-field attribution). A generation (epoch) counter on
  the pooled state turns any use-after-terminal into a no-op; a second
  `Line.Msg` no longer nil-panics either. Zero measured cost.
- **Data races** — `SensitiveFieldRegistry` hit/miss/check counters (bumped on
  the query path under at most an RLock) are now atomic; the HTTP and file
  adapters' runtime-swappable formatters are now behind `atomic.Pointer`, so
  `SetFormatter` can no longer tear the interface value an in-flight write is
  reading.
- **HTTP retry aliasing** — re-queued failed entries aliased a pooled slice
  that the next flush overwrites; they are copied out before re-queueing. The
  never-actually-used `http.Request` pool was removed, and the per-entry
  scratch buffer is now reused across a batch.
- **Non-finite floats** — NaN/±Inf rendered as bare `NaN`/`+Inf` (invalid
  JSON); they now render as quoted strings, matching zerolog. Matters more
  under Go 1.27, whose json/v2-backed parsers are stricter.
- **Pre-epoch timestamps** — negative unix-nanos truncated toward zero and
  rendered garbage fractional digits (`.+00`); seconds now floor-divide with a
  non-negative remainder.
- **`processExists` never worked** — it sent a nil `os.Signal`, which the os
  package rejects on every platform, so live processes were judged dead and
  their file locks stolen as "stale". Unix now probes with `signal 0` (EPERM
  counts as alive), Windows opens a process handle, and other platforms
  conservatively never break locks.
- **Rotation shutdown race** — the async compress/cleanup goroutine is now
  tracked by the adapter's WaitGroup, so `Close` waits for it instead of racing
  file teardown.
- **Fatal/Panic semantics** — `Fatal` now writes, flushes, and exits (code 1,
  overridable via `Config.ExitFunc`); `Panic` writes and panics — the
  conventional contract shared by zap/zerolog/logrus/stdlib. Previously both
  just logged.
- **Consistency sweep** — the discard fast path and metrics counting are now
  symmetric across ALL levels (previously Info had a private shortcut and
  Debug/Error/Trace/Fatal/Panic skipped metrics); pooled entries re-establish
  their `StaticFields` backing on acquire and clear used field values on
  release (no stale-PII retention); `Flush`/`Close` aggregate all adapter
  errors with `errors.Join` instead of stopping at the first; ~80 inert
  `//go:inline` pseudo-directives (not a real compiler directive) removed.

### Added (features)
- **`halologgen` — schema-to-facade code generation** (`cmd/halologgen`).
  A YAML schema names every loggable field with its type; the generator
  emits a facade package where each field is a typed method on level-first
  line builders and a context builder — a misspelled key or wrong-typed
  value is a COMPILE error. Key escaping is computed at generation time and
  frozen by a generated test that fails the consumer's build if the runtime
  escaper ever diverges. Deterministic output (regenerate-and-diff test
  against the committed `examples/applog`), 8 validation rules covered,
  and the generated hot path is guarded at 0 allocs/op.
- **Context branching fix (copy-on-append)** — branching a Context with
  spare backing capacity let the second branch overwrite the first branch's
  bound field (deterministic for children of bound loggers; the byte form
  could tear). Confirmed test-first and fixed
  by capacity-clamping both appends so every Context value is
  immutable-in-effect. Also: `Context.WithAny` for setter symmetry, and
  `otelbridge.Bind` now binds `trace_flags` alongside trace/span IDs,
  completing the OTel correlation convention.
- **Contextual (child) loggers** — `logger.With().Str(...).WithString(...).Logger()`
  binds fields once: encoded to final bytes at derivation, emitted as ONE
  memcpy per line on the direct path, prepended as structured (maskable) data
  on the capture path, byte-identical either way and 0 allocs/op per line
  (guarded). Chains, inherits level at derivation, capped at 32 bound fields.
  Measured on the request-logging shape (5 bound + 1 call-site field):
  37 ns/op vs phuslu 55, zerolog 91, zap 180 (which also allocates). Fixed in
  passing: on direct-eligible loggers, `Typed().Fatal/Panic` wrote the line
  but skipped the flush-exit/panic contract — terminal semantics now sit on
  the shared dispatch tail (regression-tested).
- **Backpressure sampling** — `sampling.NewBackpressureSampler(ring, low, high)`
  reads the async ring's live occupancy (new `Occupancy()` on the ring
  adapter) and sheds Trace–Warn lines proportionally to pipeline fill:
  nothing below the low watermark, linear down to keep-1-in-16 at the high
  watermark, Error+ always passes. O(1), allocation-free, deterministic
  (counter, not RNG) so drops spread evenly.
- **OpenTelemetry trace correlation** (`otelbridge`, separate Go module — the
  core logger gains no OTel dependency): `otelbridge.Bind(ctx, logger)`
  derives a child carrying `trace_id`/`span_id` hex-encoded once; each
  correlated line is 0 allocs/op (guarded).
- **Removed the dead `CoreLogger` god-interface** from `types` — 60 methods
  promising an unimplemented API (Colorize, Tracef, ToFile, …) with zero
  references; the real surface is the concrete `core.Logger` plus the small
  purpose-built interfaces.

### Changed (performance)
- **Per-type direct append — the FieldValue funnel is gone from the fast
  path.** Profiling showed 72% of a ten-field line spent building and
  funneling a 56-byte value box per field. Typed and Line setters for known
  types now append `,"key":value` bytes in one call on the direct path
  (`jsonfmt.Append{String,Int,Float64,Bool}Field`); the box is built only for
  capture. Ten-field typed 177→105 ns, twenty-field 328→172 ns (linux/amd64
  medians), byte output unchanged, 0 allocs/op.
- **Message-only lines take the direct byte path.** With a single raw-capable
  JSON adapter, `Info(msg)` renders fused-header + closer straight into the
  pooled line buffer — no LogEntry. Bare message 49→24 ns; a seventh
  allocation guard pins the path at 0 allocs/op.
- **Result:** HaloLog wins every scenario of the six-logger comparison on
  linux/amd64 — including phuslu/log, the public-leaderboard leader — see
  `benchmarks/comprehensive_comparison.md` (which now supersedes the stale
  pre-v1.0 result files in that directory).
- **Console adapter: formatting moved outside the write lock, formatter behind
  an atomic pointer.** The mutex now guards only the single `Write` call, so
  concurrent loggers contend for nanoseconds instead of encoding time, and
  `DirectEncoder()` — queried once per line on the typed/keyed fast path — is a
  lock-free atomic load instead of a mutex pair. Measured: one-field typed
  lines ~54→46 ns, ten-field typed ~184→174 ns (win/amd64); zero allocations
  unchanged.
- **Benchmarks: phuslu/log added to the comparison field** (the fastest
  public Go logger on current leaderboards) across the bare-message, one-,
  ten-, and twenty-field scenarios, same JSON-to-io.Discard footing.

### Added
- **Edge-case regression suite** (`tests/edge`) — 19 public-API tests pinning
  the hardening pass: level filtering on every fluent API, sampler
  consultation, JSON validity under hostile values (NaN/±Inf, control chars,
  invalid UTF-8-adjacent input, 2 MiB payloads, 100-field lines), builder
  use-after-terminal misuse, Fatal/Panic semantics, pre-epoch timestamps, and
  concurrent line integrity.
- **`log/slog` bridge** (`slogbridge`) — adopt HaloLog in slog-first codebases
  with one line: `slog.SetDefault(slog.New(slogbridge.New(logger)))`. Validated
  against the standard library conformance suite (`testing/slogtest`); groups are
  encoded as dot-joined key prefixes; one documented deviation (HaloLog stamps
  its own clock time on every line, so a zero `Record.Time` is never rendered).
- **Level-first Line API** — `logger.InfoLine().Str(key, v).WithInt(...).Msg(m)`
  fixes the level when the line opens, so a disabled level costs a single check:
  no state acquisition, no field encoding, zero allocations.
- **Timestamp precision option** — `json.NewJsonFormatterWithPrecision`
  (second/milli/micro/nano). The strategy is chosen once at construction; every
  precision renders with zero allocations. Sub-second accuracy is bounded by the
  cached clock's refresh interval (~10ms by default) — documented, not hidden.
- **Direct-append fast path** — with a single raw-capable JSON adapter and no
  masking/sampling, the typed and Line builders encode fields straight to bytes
  (statically dispatched, zerolog-style); pre-declared keys emit as one memcpy.
  Output is byte-identical to the capture path (test-pinned) and every other
  configuration falls back automatically, so masking can never be bypassed.
- **Fused header cache** — at second precision the entire
  `{"time":..,"level":..` header is served per (second, level) from a lock-free
  cache: one memcpy per line.
- **Benchmark CI** — a Linux workflow gates every zero-allocation guard and
  publishes the co-measured multi-logger numbers per commit.


- **Pre-declared field keys** (`halolog.Key`, typed builder `Str/Int/Bool/…`
  methods) whose JSON escaping is computed once, so the hot path emits a key with
  a single copy and no escaping or lookup — measured ~25% faster than plain string
  keys on field-heavy lines. Plain string keys remain escaped inline (fast and
  allocation-free for the short keys typical of logging).
- **Async ring adapter** (`adapters/outputs/asyncring`) — a bounded, lock-free
  multi-producer/single-consumer ring that moves serialization and I/O off the
  caller's goroutine for low, predictable caller latency. Records are copied into
  ring-owned storage; `OnFull` selects drop (default, wait-free) or block; `Close`
  drains losslessly.

### Changed
- The JSON formatter's small-integer cache now covers 0–511 (small counts, ports,
  the HTTP status range) and the typed and boxed int/uint field paths use it.

### Fixed
- **UTC timestamp corruption on cache hits** — the old cache-hit path appended a
  hardcoded 25 bytes, corrupting 20-byte `"Z"`-suffixed UTC timestamps (it would
  have fired on any UTC machine). The header engine caches the true length.
- **Pool memory pinning** — one pathological multi-megabyte line can no longer
  pin its grown buffer on the per-P pool: retained direct-path buffers are
  capped at 64KiB.
- **`adapters/middleware` async use-after-recycle** — the channel-based async
  adapter enqueued a pointer to the caller's pooled entry, which the logger
  recycles immediately, so the background writer could serialize an overwritten
  record. Each write now copies the record into a pooled, detached entry
  (allocation-free after warmup); the new lock-free `asyncring` adapter is the
  recommended high-throughput variant.

## [1.0.0] - 2026-07-07

Internal 1.0.0 milestone: a zero-allocation structured logging library for Go.

### Added
- Console/stdout output adapter (`adapters/outputs/console`) — thread-safe and
  buffer-pooled — restoring basic console logging.
- A permanent zero-allocation guard (`TestZeroAlloc_Info` /
  `TestZeroAlloc_WithFields`) that fails the suite if the hot path ever allocates.
- Reusable CI (lint + race tests + coverage gate on Linux & Windows), release
  and goreleaser configuration.

### Fixed
- **`pool.EntryPool.Close()` infinite-loop hang** — it drained a `sync.Pool`
  waiting for `Get()` to return `nil`, but a pool with a non-nil `New` func never
  returns `nil`, so `Close()` (reachable via `Shutdown()`) spun forever. The
  drain loop is removed (a `sync.Pool` needs no manual draining).
- **`EnhancedPipelineStrategy.UpdateConfiguration` self-deadlock** — it held
  `ps.mu` while calling `selectOptimalPipeline`, which re-acquired the same
  non-reentrant mutex. Selection now has an unlocked inner variant.
- **`EnhancedPipelineStrategy` `atomic.Value` panic** — the three concrete
  pipeline types were stored into one `atomic.Value`, which panics on a type
  change (triggered by reconfiguration). They are now boxed in a single wrapper.
- **Field-capture bugs** — `Entry.ToLogEntry` dropped all fields when the target
  had no backing buffer; the extracted pool returned zero-capacity static buffers
  and its field builder never wrote fields. All fixed.

### Changed
- **Reconciled the mid-refactor API.** `core` had been migrated from a registry
  (`GetLogger`) to a fluent builder (`core.New()...MustBuild()`), leaving the
  root and CLI calling removed functions. The root `halolog.GetLogger`/
  `GetLoggerWithConfig` are reimplemented as a name-keyed singleton registry over
  the real builder; the benchmark CLI, version shims, examples and sandbox were
  parked out of the shipped tree.
- **Removed dead scaffolding**: the `HaloLogger` god-object (a 45-field exported
  struct never constructed) and the `LoggerProvider`/`LoggerFactory`/
  `LoggerRegistry` interfaces that only existed to return it. The real logger is
  `core.Logger`.
- Renamed the stuttering `json.JSONFormatter` / `text.TextFormatter` to
  `json.Formatter` / `text.Formatter`.

### Performance
- Hot path remains (and improved to) **0 allocations per call**: `Info`
  ~2 ns/op, a two-field `WithField` chain ~29 ns/op, both **0 B/op, 0 allocs/op**.

### Quality
- `golangci-lint` reports zero issues; build, vet, race and the full test suite
  are green.
