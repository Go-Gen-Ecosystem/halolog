# OpenTelemetry bridge validation boundary

This document records the validation applied to the `otelbridge` candidate in
pull request #1. It is evidence for a specific integration, not a claim that an
OpenTelemetry deployment is universally allocation-free or production-certified.

## Contracts under test

- `Enabled` and `Emit` receive the same reconstructed correlation context.
- The final occurrence of `trace_id`, `span_id`, or `trace_flags` is
  authoritative. A final invalid, wrongly typed, or masked value clears prior
  parsed data and remains visible as one attribute.
- Values are not evaluated before `Enabled`; shadowed error metadata is never
  evaluated. A disabled byte payload is not copied.
- Emitted byte payloads own their storage after the caller reuses its entry and
  buffer. This intentional ownership copy allocates.
- Explicit providers and paired flush callbacks are drained with a cooperative
  deadline. `Close` never shuts down a caller-owned provider. Late global
  delegation without a paired drain target returns `ErrFlushUnavailable`.
- The bridge consumes the published core v1.0.2 masking contract without a
  module replacement or runtime test skips.

## Executed matrix

| Environment | Dependency mode | Normal | Race | Runtime skips |
| --- | --- | ---: | ---: | ---: |
| Windows/amd64, Go 1.27.1 | published v1.0.2 | 64 top-level + 137 subtests | targeted | 0 |
| Windows/amd64, Go 1.24.0 | published v1.0.2 | 64 top-level + 137 subtests | targeted | 0 |
| Linux/amd64, Go 1.27.1 | published v1.0.2 | 64 top-level + 137 subtests | 61 top-level + 107 subtests | 0 |
| Linux/amd64, Go 1.27.1 | local integrated core | 64 top-level + 137 subtests | 61 top-level + 107 subtests | 0 |

Counts include grouped subtests; they are not counts of independent security
properties. CI repeats the local/published matrix with Go 1.24.0 and stable.
Allocation tests run without race or coverage instrumentation; correctness and
retention tests remain in the race build.

Four negative controls were also executed:

- replacing the byte-ownership copy with aliasing caused all six retained-record
  width cases to fail;
- selecting core v1.0.1 caused all three consumer masking contracts to fail.
- probing `Enabled` with `context.Background()` caused the sampled real-SDK case
  to fail;
- eagerly resolving unrelated fields caused both disabled and enabled callback
  count contracts to fail.

## Allocation and timing boundary

Local measurements used Go 1.27.1 on windows/amd64, an Intel Core Ultra 9
285HX, one logical CPU, five one-second runs, and a discarding OpenTelemetry API
logger. The medians below exclude SDK processing, export, collector, network and
backend costs.

| Entry shape | ns/op | B/op | allocs/op | committed ceiling |
| --- | ---: | ---: | ---: | ---: |
| no fields | 34.56 | 0 | 0 | 0 |
| 5 integer fields | 185.4 | 0 | 0 | 0 |
| 6 integer fields | 232.4 | 48 | 1 | 1 |
| 9 integer fields | 330.1 | 160 | 1 | 1 |
| 16 integer fields | 544.1 | 448 | 1 | 1 |
| 17 integer fields | 631.2 | 1,184 | 2 | 2 |
| 2 context + 2 integer fields | 158.5 | 0 | 0 | 0 |
| 2 indexed + 2 integer fields | 197.8 | 0 | 0 | 0 |
| 4 KiB `[]byte` payload (owned copy) | 382.1 | 4,096 | 1 | 1 |
| 2 integer fields + correlation | 250.4 | 128 | 2 | 2 |

The 4 KiB disabled-byte workload measured 25.43 ns/op and 0 B/op,
0 allocs/op. Emitting the same payload deliberately copies exactly 4,096 bytes
and has a one-allocation ceiling. Arbitrary values rendered through
`fmt.Sprint`, SDK processors, exporters and user callbacks have separate costs.

## Reproduction

```sh
# From the repository root, with the supported toolchain on PATH.
bash scripts/verify-otelbridge.sh published
bash scripts/verify-otelbridge.sh local

cd otelbridge
GOWORK=off go test -count=1 ./...
GOWORK=off go test -race -skip PipelineScale -count=1 ./...
GOWORK=off go test -run '^$' -bench . -benchmem -benchtime=1s -count=5
```

`scripts/verify-otelbridge.sh` rejects module replacements in published mode,
requires the consumer contracts by name, runs `go vet`, rejects runtime skips,
and verifies that `go.mod` and `go.sum` remain unchanged.

## Explicit limits

This matrix does not include a remote OTLP collector, TLS failures, network
partitions, backend acknowledgement semantics, prolonged soak tests, ARM64, or
every custom processor. A context deadline is cooperative and cannot preempt a
provider that ignores cancellation. The SDK's bounded batch queue may drop under
pressure; the backpressure test verifies deadline reporting and recovery, not
lossless delivery.
