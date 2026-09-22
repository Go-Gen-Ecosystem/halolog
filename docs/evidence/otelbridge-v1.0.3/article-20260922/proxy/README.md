# Proxy-only consumer check for `otelbridge/v1.0.3`

Captured on 2026-09-22 with Go 1.27.1 on Windows/amd64. This is a
post-release installation check, not a release-time benchmark or a test of an
OTLP collector.

The [`consumer/`](consumer/) fixture is an independent module. It has no
`replace` directive and constructs the published adapter in a test. The run
used new dedicated module/build/GOPATH cache directories outside the consumer
module, with these environment settings:

```text
GOENV=off
GOWORK=off
GOTOOLCHAIN=local
GOPROXY=https://proxy.golang.org
GOSUMDB=sum.golang.org
GOPRIVATE=
GONOPROXY=
GONOSUMDB=
GOMODCACHE=<new cache>/mod
GOCACHE=<new cache>/build
GOPATH=<new cache>/gopath
```

From `consumer/`, with the above environment:

```bash
go version
go mod download all
go test -mod=readonly -v -count=1 ./...
go mod verify
go list -m -json github.com/go-gen-ecosystem/halolog/otelbridge
go list -m -json github.com/go-gen-ecosystem/halolog
go mod edit -json
```

The unedited command output is in
[`consumer-transcript.txt`](consumer-transcript.txt). The test passed,
`go mod verify` reported `all modules verified`, the bridge resolved to
`v1.0.3`, and the selected core was `v1.0.2`. The selected module graph has no
replacement. The verified module sums are in [`consumer/go.sum`](consumer/go.sum).

This check proves that this particular clean Go consumer can resolve and
construct the released adapter through the public proxy. It does not measure
performance, prove all platform combinations, or exercise SDK export.
