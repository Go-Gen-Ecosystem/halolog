# Released adapter allocation replay

Captured on 2026-09-22 from the **public-proxy module ZIP** for
`github.com/go-gen-ecosystem/halolog/otelbridge@v1.0.3`, not from the dirty
repository checkout. The tag resolves to commit
`864b6ff479eefe446af92c4476b369a75b4236b3`; the downloaded module sum
is `h1:4VjdA5TC7+rc4/R5crIZ/+73vuakLqCjPT+j14Scxcw=`.

- Host: Windows/amd64, Intel Core Ultra 9 285HX.
- Toolchain: Go 1.27.1.
- Invocation from the extracted module: `go test -run '^$' -bench '^BenchmarkAdapterWrite$' -benchmem -benchtime=200ms -count=5`.
- Environment: `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local`,
  `GOPROXY=https://proxy.golang.org`, `GOSUMDB=sum.golang.org`; dedicated
  `GOMODCACHE`, `GOCACHE`, and `GOPATH` outside the extracted module.
- Raw output: [`adapter-write-go1.27.1-windows-amd64-count5.txt`](adapter-write-go1.27.1-windows-amd64-count5.txt).
- Disabled-byte comparison: the same environment with
  `-bench '^BenchmarkAdapterWriteDisabledBytePayload$'`, captured in
  [`disabled-byte-payload-go1.27.1-windows-amd64-count5.txt`](disabled-byte-payload-go1.27.1-windows-amd64-count5.txt).

For every named shape, all five `B/op` and `allocs/op` readings agreed. In
particular, five scalar fields were `0/0`; 6, 9, 16, 17, and 32 were one
allocation; 33 fields were two; an emitted 4 KiB byte slice copied 4,096
bytes in one allocation; the same payload rejected by `Enabled` stayed at
`0 B/op` and `0 allocs/op`; the correlated case used 128 bytes/two allocations.
The benchmark source and separate allocation guards are in the same public
module tag.

The `ns/op` readings varied substantially for some shapes over these short
200 ms runs. This capture supports the article's **allocation table**, not a
stable latency comparison or an end-to-end OTel throughput claim. The adapter
receives a prebuilt `LogEntry` and writes to a discarding API logger; SDK,
exporter, collector, network, and backend are excluded.
