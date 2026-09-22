<p align="center">
  <img src="assets/halolog-logo.svg" alt="The HaloLog mark: a glowing halo ring" width="140">
</p>

# HaloLog

[![Go Version](https://img.shields.io/badge/go-1.24+-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)
[![Performance](https://img.shields.io/badge/bare%20message-23.9%20ns%2Fop-red.svg)](benchmarks/comprehensive_comparison.md)
[![Zero Allocation](<https://img.shields.io/badge/allocation-zero%20(0%20B%2Fop)-brightgreen.svg>)](docs/PERFORMANCE.md)

HaloLog is a structured logging library for Go built around a zero-allocation
hot path. It logs a full JSON line in 23.9 ns on the benchmark host, roughly
42 million lines per second on one goroutine, and allocates nothing while
doing it. The allocation claim is not a slogan: committed guard tests fail
the build if any hot path ever allocates.

## Features

- **Zero-allocation hot path.** 23.9 ns/op for a bare message and 0 B/op in
  every measured scenario, enforced by eight committed guard tests.
- **Structured logging** with typed field methods and automatic inference.
- **Output adapters** for console, file with rotation, HTTP batching, syslog,
  and a lock-free async ring, plus an interface for custom destinations.
- **PII masking** with regex patterns, applied on a path that fast-path
  optimizations cannot bypass.
- **Field-level encryption** (AES-256-GCM) as a building block for stricter
  data-handling policies.
- **Sampling**, including a backpressure sampler that sheds load based on how
  full the async pipeline actually is.
- **Alerting** to Slack, PagerDuty, and webhooks.
- **Configuration** from YAML, JSON, or environment variables.
- **Thread safety** without a global lock, using per-P pooled state.

## Installation

```bash
go get github.com/go-gen-ecosystem/halolog
```

## Quick start

```go
package main

import (
    "github.com/go-gen-ecosystem/halolog"
)

func main() {
    // Named singleton logger; no cleanup needed.
    logger := halolog.GetLogger("my-app")

    logger.Info("Server started")
    logger.Debug("Debug message")
    logger.Warn("Warning message")
    logger.Error("Error occurred")

    logger.WithField("user_id", 12345).
        WithField("action", "login").
        Info("User logged in")

    logger.WithError(err).
        WithField("operation", "database_query").
        Error("Database operation failed")
}
```

### Field keys: plain strings or pre-declared keys

HaloLog offers two ways to attach a field. Both are correct and both are
zero-allocation, so the choice is about how hot the call site is.

Plain string keys are the ergonomic default. `WithField` and `WithString`
take an ordinary string, and the JSON formatter escapes it inline. For the
short keys typical of logging this is already fast, so use it anywhere
convenience matters.

```go
logger.WithField("user_id", 12345).WithField("action", "login").Info("login")
```

Pre-declared keys are for the hottest loops. Declare each key once, usually
as a package-level var. Its escaping is computed a single time, and the hot
path then emits it with one copy and no lookup at all. This is the same
pattern zerolog and zap users know as pre-declared fields.

```go
// declared once, reused forever
var (
    userID = halolog.Key("user_id")
    action = halolog.Key("action")
)

logger.Typed().Str(userID, "alice").Str(action, "login").Info("login")
// keyed typed methods: Str, Int, Int64, Float64, Bool, Err, Any
```

The rule of thumb: reach for `halolog.Key(...)` in tight, high-frequency
logging loops, and use string keys everywhere else. Neither allocates.

### Contextual child loggers

Bind fields once and log them on every line. The bound context is encoded to
its final bytes a single time, when you call `Logger()`. After that, each
line emits the whole context as one memcpy instead of re-encoding it. This
is the shape of real service logging, where every request or tenant gets its
own logger, and it is the measured scenario where HaloLog leads by the
widest margin: 37 ns/op with five bound fields plus a call-site field,
against 55 for phuslu, 91 for zerolog, and 180 for zap on the same host.

```go
reqLog := logger.With().
    Str(keyTenant, "acme").
    WithString("region", "eu-west-1").
    WithInt("shard", 7).
    Logger()

reqLog.Info("accepted")                            // context rides along
reqLog.Typed().WithInt("status", 200).Info("done") // composes with per-line fields
child := reqLog.With().WithString("op", "billing").Logger() // chains
```

Bound fields stay visible to PII masking, because they travel as structured
data on the masked path; binding is never a masking bypass. They appear
before per-line fields, are capped at 32 per logger, and every line remains
0 allocs/op under a dedicated guard. A child inherits its parent's level at
derivation.

### Schema-checked logging facades (halologgen)

Declare your loggable fields once in a schema, then generate a facade where
every field is a typed method. A misspelled key or wrong-typed value fails
the build instead of corrupting a log line. Key escaping is computed at
generation time and frozen by a generated test, and a committed test
regenerates the example and byte-compares the output, so the generator is
provably deterministic.

```yaml
# logging.yaml
package: applog
fields:
  - { name: user_id, type: string, ident: UserID }
  - { name: status,  type: int }
```

```bash
go run github.com/go-gen-ecosystem/halolog/cmd/halologgen -schema logging.yaml -out ./applog
```

```go
log := applog.Wrap(coreLogger)
log.Info().UserID("alice").Status(200).Msg("handled")   // compile-checked
reqLog := log.With().UserID("alice").Logger()           // schema-typed contexts too
```

Generated facades ride the same hot paths as handwritten calls and stay at
0 allocs/op. See `examples/applog/` for a complete generated package with
its determinism and behavior tests.

### Level-first lines

`InfoLine`, `DebugLine`, `WarnLine`, and `ErrorLine` fix the level when the
line opens, so a filtered-out level costs a single check. There is no state
and no encoding for suppressed lines.

```go
logger.InfoLine().Str(keyUser, "alice").WithInt("status", 200).Msg("handled")
logger.DebugLine().WithString("dump", expensive()).Msg("trace") // ~1ns when Debug is off*
```

\* that is the level check itself; argument evaluation is still yours to guard.

### Using HaloLog from log/slog

Codebases written against the standard library's `slog` can switch backends
with one line, and every existing call site keeps working:

```go
import "github.com/go-gen-ecosystem/halolog/slogbridge"

slog.SetDefault(slog.New(slogbridge.New(logger)))
slog.Info("handled", "status", 200, slog.Group("req", "id", "abc"))
// {"time":"...","level":"INFO","message":"handled","status":200,"req.id":"abc"}
```

The bridge passes the standard library's `testing/slogtest` conformance
suite. Groups are dot-joined, and HaloLog stamps its own clock time on every
line.

### OpenTelemetry trace correlation

The `otelbridge` module ships with its own `go.mod`, so the core logger
takes no OpenTelemetry dependency. It derives a child logger carrying
`trace_id`, `span_id`, and `trace_flags`, hex-encoded once at bind time.
Every line in the request then pays a single memcpy for its correlation
fields, at 0 allocs/op.

```go
import "github.com/go-gen-ecosystem/halolog/otelbridge"

func handle(w http.ResponseWriter, r *http.Request) {
    log := otelbridge.Bind(r.Context(), baseLogger) // no span? returns baseLogger
    log.Info("handling")  // ...,"trace_id":"4bf9...","span_id":"00f0..."
}
```

### Routing logs to OpenTelemetry

`otelbridge.NewAdapter` is an output adapter that emits each entry into the
OpenTelemetry Logs API, so the same line reaches an OTLP backend as a
LogRecord while the console adapter keeps writing it to stderr. Register
both and the logger fans out to each in turn:

```go
logger := core.New().
    Adapters(
        console.New(),
        otelbridge.NewAdapter("github.com/acme/checkout",
            otelbridge.WithLoggerProvider(provider)),
    ).
    MustBuild()

log := otelbridge.Bind(r.Context(), logger)
log.Typed().WithInt("status", 200).Info("handled")
```

Fields, context, component, `error`, and the caller's file and line become
record attributes; the level maps onto the OpenTelemetry severity scale. A
logged field wins over metadata the adapter would derive under the same key,
so a field named `component` or `error` is never emitted twice.

`Bind`'s correlation fields are consumed rather than copied: the adapter
parses them back into a span context, so the record carries a real TraceID
and SpanID — what a backend such as Honeycomb correlates on — instead of
three attributes it cannot join against. A valid trace id is the only
requirement; the data model allows a record that names its trace without
naming a span, so a trace id still correlates when the span id is absent.
Anything that does not parse is not consumed and stays in the attributes,
where it is still visible. For duplicate correlation keys, the final occurrence
is authoritative; a final malformed or masked value cannot revive an earlier
identifier. Without a preceding `Bind` the record is emitted uncorrelated.

The following are medians of five one-second local runs on Go 1.27.1,
windows/amd64, Intel Core Ultra 9 285HX, against a discarding API logger. They
measure the adapter, not an SDK, exporter, collector, network, or backend. The
committed allocation tests are ceilings; these measured timings are not CI
thresholds. The numbered field rows use integer scalar fields.

| Record shape | ns/op | B/op | allocs |
| --- | --- | --- | --- |
| no fields | 34.56 | 0 | 0 |
| 5 scalar fields (`log.Record` inline capacity) | 185.4 | 0 | 0 |
| 6 scalar fields | 232.4 | 48 | 1 |
| 9 scalar fields | 330.1 | 160 | 1 |
| 16 scalar fields | 544.1 | 448 | 1 |
| 17 scalar fields | 631.2 | 1,184 | 2 |
| 2 context fields plus 2 scalar fields | 158.5 | 0 | 0 |
| 2 indexed fields plus 2 scalar fields | 197.8 | 0 | 0 |
| 4 KiB `[]byte` payload (owned copy) | 382.1 | 4,096 | 1 |
| 2 scalar fields plus trace correlation | 250.4 | 128 | 2 |

A severity the SDK drops skips the attribute work but still pays for the
span context: `Enabled` has to be asked under the same correlation context
`Emit` would use, or a processor filtering on the sampled flag answers for a
record that is not the one being emitted. A real SDK adds its own cost. An
emitted `[]byte` attribute is deliberately copied so queued records cannot
alias caller memory; that ownership guarantee costs one allocation and exactly
the payload size for a single 4 KiB value. A disabled 4 KiB payload is not
converted or copied; its measured median was 25.43 ns/op, 0 B/op, 0 allocs/op.
Arbitrary interface values
may allocate in their formatter and are not covered by scalar-field ceilings.

`Flush` and `Close` drain an explicitly supplied provider through its
`ForceFlush`. That is not a convenience: `Fatal` writes its line, calls
`Logger.Flush`, and exits the
process from inside the logging call, so a batching processor's queue would
otherwise swallow the last line a program ever writes.

The wait is bounded — `DefaultFlushTimeout` is 5s, `WithFlushTimeout` sets
your own, and a non-positive value opts into waiting indefinitely. The bound
is what stops a wedged exporter holding the process open: a drain that times
out returns the error and lets `Fatal` reach its exit. Neither method shuts
the provider down; it belongs to the caller and is usually shared with
tracing, so `Shutdown` stays theirs to call.

The default global proxy can delegate emission to a provider installed later,
but it cannot identify that provider's optional `ForceFlush`. In that case
`Flush` returns `ErrFlushUnavailable`. Supply the provider explicitly, or pair
late global initialization with `WithFlushFunc(provider.ForceFlush)`. The
callback stays bound to the emission target even if the global is replaced.
The timeout is cooperative: provider code that ignores context cancellation
cannot be forcibly preempted.

The complete validation boundary and reproduction commands are documented in
[`docs/otelbridge-validation.md`](docs/otelbridge-validation.md).

### Timestamp precision

The JSON formatter renders whole seconds by default, which is the fastest
option because the header is a single cached memcpy. For trace correlation,
pick a sub-second resolution:

```go
f := json.NewJsonFormatterWithPrecision(json.PrecisionMilli) // .123
// PrecisionSecond | PrecisionMilli | PrecisionMicro | PrecisionNano
```

Every precision is zero-allocation. One honest bound: the default cached
clock refreshes every 10 ms, so displayed sub-second digits can carry up to
about 10 ms of wall-clock skew. That is sufficient for ordering within a
service; run a finer `cache.NewCachedClock` interval if you need tighter
accuracy.

### Custom configuration

```go
import (
    "github.com/go-gen-ecosystem/halolog"
    "github.com/go-gen-ecosystem/halolog/config"
    "github.com/go-gen-ecosystem/halolog/types"
)

cfg := &config.ImmutableConfig{
    Level:         types.InfoLevel,
    EnableMetrics: true,
}
logger := halolog.GetLoggerWithConfig("my-app", cfg)
```

## Output adapters

### Console

```go
import "github.com/go-gen-ecosystem/halolog/adapters/outputs/console"

adapter := console.New()                          // writes to os.Stdout
// adapter := console.NewWithWriter(w, formatter) // custom writer / formatter
```

### File with rotation

```go
import "github.com/go-gen-ecosystem/halolog/adapters/outputs/file"

adapter, _ := file.NewFileAdapter("app.log", &file.RotationConfig{
    MaxSize:    100 * 1024 * 1024, // 100MB
    MaxBackups: 10,
    Compress:   true,
})
```

### HTTP/webhook

```go
import "github.com/go-gen-ecosystem/halolog/adapters/outputs/http"

adapter := http.NewHTTPAdapterWithOptions(&http.HTTPAdapterOptions{
    URL:           "https://logs.example.com/ingest",
    BatchSize:     100,
    FlushInterval: 5 * time.Second,
})
```

### Async ring

Wrap any destination to move serialization and I/O off the calling
goroutine. Producers copy each record into a bounded, lock-free ring and
return immediately, and a single background goroutine serializes and writes.
This optimizes for low, predictable caller latency rather than total
throughput, which is bounded by the one writer. The record is copied into
ring-owned storage, so it stays safe even though the logger recycles its
entry immediately.

```go
import (
    "os"
    "github.com/go-gen-ecosystem/halolog/adapters/outputs/asyncring"
    jsonfmt "github.com/go-gen-ecosystem/halolog/adapters/formatters/json"
)

adapter, _ := asyncring.New(asyncring.Options{
    Writer:    os.Stdout,
    Formatter: jsonfmt.NewJsonFormatter(),
    Capacity:  1024,             // rounded up to a power of two
    OnFull:    asyncring.Drop,   // or asyncring.Block
})
defer adapter.Close()           // drains everything already accepted
// adapter.Dropped() reports records dropped under overload (OnFull=Drop)
```

One caveat: field values are captured by shallow copy. Log values rather
than mutable references, because a `WithField("x", &mutableStruct)` may be
serialized later by the background goroutine.

### Syslog (Unix)

```go
import "github.com/go-gen-ecosystem/halolog/adapters/outputs/syslog"

adapter := syslog.NewSyslogAdapterWithOptions(&syslog.SyslogAdapterOptions{
    Network: "udp",
    Address: "localhost:514",
    Tag:     "myapp",
})
```

On Windows the adapter compiles as a stub whose operations return
`ErrSyslogNotSupported`.

## Security features

### PII masking

```go
import "github.com/go-gen-ecosystem/halolog/masking"

masker := masking.NewPIIMasker()
masker.AddPattern("email", `\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`, "[EMAIL]")
masker.AddPattern("ssn", `\b\d{3}-\d{2}-\d{4}\b`, "[SSN]")
```

### Field encryption

The masking package includes an AES-256-GCM field encryptor for
selected sensitive values:

```go
masking.InitGlobalEncryptor("your-32-byte-secret-key")
```

See [docs/SECURITY.md](docs/SECURITY.md) for the security policy, what the
building blocks do and do not guarantee, and how masking and encryption fit
the logging pipeline.

## Sampling

Wire a sampler into the logger with `Sampling(...)`. Every Trace through
Error line is then offered to `ShouldSample` before it is written, while
Fatal and Panic lines are never sampled away, so the last line before a
crash always lands. Configuring a sampler keeps the capture path, since each
line must be inspected, which disables the direct-append fast path.

```go
logger := core.New().
    Level(types.InfoLevel).
    Adapter(myAdapter).
    Sampling(sampler).
    MustBuild()
```

### Backpressure sampling

Classic samplers drop a fixed fraction whether or not the pipeline is
keeping up. `BackpressureSampler` closes the loop instead: it reads the
async ring's live occupancy and sheds Trace through Warn lines in proportion
to how full the pipeline actually is. Nothing is dropped below the low
watermark, shedding increases linearly down to keep-1-in-16 at the high
watermark, and Error and above always pass, because the lines an operator
needs most are the ones an overloaded system emits. Decisions are O(1),
allocation-free, and deterministic, so drops spread evenly instead of
clustering.

```go
ring, _ := asyncring.New(asyncring.Options{Writer: f, Formatter: jsonfmt.NewJsonFormatter(), Capacity: 4096})
logger := core.New().
    Adapter(ring).
    Sampling(sampling.NewBackpressureSampler(ring, 0.5, 0.9)). // watermarks: fill fractions
    MustBuild()
```

### Count-based sampling

```go
import "github.com/go-gen-ecosystem/halolog/sampling"

sampler := sampling.NewSamplingManager(sampling.SamplingConfig{
    Strategy:            sampling.SampleByCount,
    SamplingDenominator: 10, // sample 1 in 10
})
```

### Adaptive sampling

```go
sampler := sampling.NewAdaptiveSampler(sampling.AdaptiveConfig{
    BaseRate:         0.1,   // 10% base rate
    ErrorBoost:       10.0,  // 10x for errors
    TargetThroughput: 10000, // adjust based on load
})
```

## Alert integration

### Slack

```go
import "github.com/go-gen-ecosystem/halolog/alerts"

sender := alerts.NewSlackSender(alerts.SlackAlertConfig{
    WebhookURL: "https://hooks.slack.com/services/...",
    Channel:    "#alerts",
    RateLimit:  time.Minute,
})

sender.Send(&alerts.AlertPayload{
    Level:   "error",
    Message: "Critical error occurred",
})
```

### PagerDuty

```go
sender := alerts.NewPagerDutySender(alerts.PagerDutyAlertConfig{
    IntegrationKey: "your-integration-key",
    Severity:       "critical",
})
```

## Configuration

### YAML

```go
loader := config.NewConfigLoader()
cfg, err := loader.LoadFromYAML([]byte(`
level: info
output: multi
format: json
file:
  path: app.log
  rotation:
    max_size: 104857600
    max_age: 604800
    compress: true
alerts:
  slack:
    webhook_url: https://hooks.slack.com/services/...
    threshold: error
`))
```

### JSON

```go
cfg, err := loader.LoadFromJSON([]byte(`{
    "level": "info",
    "output": "multi",
    "format": "json",
    "masking": {
        "enabled": true,
        "patterns": [
            {"name": "email", "pattern": "\\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Z|a-z]{2,}\\b", "mask": "[EMAIL]"}
        ]
    }
}`))
```

### Environment variables

```bash
export HALOLOG_LEVEL=info
export HALOLOG_FORMAT=json
export HALOLOG_OUTPUT=multi
export HALOLOG_FILE_PATH=/var/log/myapp.log
```

## Benchmarks

Measured on Go 1.27.0, linux/amd64 (the CI environment; Intel Core Ultra 9
285HX), 5 runs of 1 s each, benchstat medians. Every logger writes a full
structured JSON line (timestamp, level, message, fields) to `io.Discard`
through the committed, fairness-audited comparison suite in `benchmarks/`,
which includes phuslu/log, the fastest logger on public Go leaderboards.
Run it yourself; numbers vary by machine.

```
                        HaloLog   phuslu   zerolog     zap     slog   logrus
Bare message            23.9 ns   63.1 ns   86.8 ns  146.2 ns  280.3  1395 ns
One field (typed)       32.0 ns   69.9 ns   99.5 ns  183.7 ns  312.9  1486 ns
Ten fields (typed)      83.6 ns  120.7 ns  201.7 ns  437.4 ns  992.9  4066 ns
Ten fields (keyed)      83.1 ns        —        —        —        —       —
Twenty fields (typed)  146.0 ns  179.9 ns  230.6 ns  394.6 ns      —       —
Disabled level         0.83 ns         —        —        —        —       —
HaloLog allocations    0 B/op, 0 allocs/op in every scenario
```

The honest summary: HaloLog wins every published scenario in the six-logger
Linux comparison and the three-logger Windows head-to-head, at 0 allocs/op,
with fully escaped keys and never-interleaved lines. It is 2.6 times faster
than phuslu on bare messages, 2.2 times at one field, 44% at ten fields, and
23% at twenty on Linux. HaloLog avoids a major source of OS-dependent latency
because it does no per-line clock reads and no syscalls on the hot path. The
full method, fairness notes, and per-platform tables are in
[benchmarks/comprehensive_comparison.md](benchmarks/comprehensive_comparison.md),
and every hot path is pinned at 0 allocs/op by eight committed guards
(`go test ./core -run TestZeroAlloc`).

Compared with its field: masking, rotation, adaptive sampling, alerting,
field encryption, and file-based configuration ship in the module, where
most loggers delegate some of these to external packages. That is a scope
difference, not a value judgment; the numbers above are the like-for-like
comparison.

### Fatal and Panic semantics

`Fatal(...)` writes the line, flushes every adapter, then calls
`os.Exit(1)`. `Panic(...)` writes the line, then panics with the message.
This is the same contract as zap, zerolog, logrus, and the standard
library. Tests and embedders can intercept termination with
`core.Config.ExitFunc`.

## Documentation

- [Performance guide](docs/PERFORMANCE.md) covers the architecture behind
  the numbers and how to keep your own call sites on the fast path.
- [Security policy](docs/SECURITY.md) covers reporting, the threat model,
  and what the masking and encryption building blocks guarantee.
- [Benchmark record](benchmarks/comprehensive_comparison.md) is the
  canonical, method-disclosed comparison against the field.
- [Documentation map](docs/DOCUMENTATION_STRUCTURE.md) explains what is
  tracked, what is staged, and the verification bar a document must pass
  before it lands here.

Getting-started, API-reference, architecture, and examples guides exist in
draft and land in `docs/` as each passes a line-by-line verification pass
against the released API.

## Testing your own logging

Point a logger at a buffer and assert on the JSON it writes:

```go
func TestUserService_LogsCreation(t *testing.T) {
    var buf bytes.Buffer
    logger := core.NewLogger(core.Config{
        Level: types.InfoLevel,
        Adapters: []types.Adapter{
            console.NewWithWriter(&buf, jsonfmt.NewJsonFormatter()),
        },
    })

    NewUserService(logger).CreateUser("test@example.com")

    var line map[string]any
    if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
        t.Fatalf("invalid log JSON: %v", err)
    }
    if line["email"] != "test@example.com" {
        t.Errorf("email = %v, want test@example.com", line["email"])
    }
}
```

## Contributing

Contributions are welcome. See the [Contributing Guide](CONTRIBUTING.md)
for the verification gate every change runs through.

## Citation

If you use HaloLog in academic or technical work, please cite it. GitHub's
"Cite this repository" button offers APA and BibTeX generated from
[CITATION.cff](CITATION.cff).

```bibtex
@software{halolog2026,
  author = {Admilson B. F. Cossa},
  title = {HaloLog: A Zero-Allocation Structured Logging Library for Go},
  year = {2026},
  url = {https://github.com/Go-Gen-Ecosystem/halolog},
  version = {1.0.1},
  license = {Apache-2.0}
}
```

## License

HaloLog is released under the [Apache License 2.0](LICENSE).
