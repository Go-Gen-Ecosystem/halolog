# HaloLog otelbridge v1.0.3 article evidence

Post-release checks captured on 2026-09-22 against the published
`otelbridge/v1.0.3` module, whose tag resolves to
`864b6ff479eefe446af92c4476b369a75b4236b3`.

- [`proxy/`](proxy/) contains a proxy-only, isolated consumer construction test,
  its fixture, raw transcript, selected module versions, and checksum result.
- [`benchmark/`](benchmark/) contains raw adapter-only allocation benchmark
  output from the downloaded release module and the exact measurement boundary.

This package does **not** contain a reliable before/after latency comparison
for the 17-field optimization. The article omits the exploratory A/B numbers
because their raw transcript and source identities were not frozen together.
The allocation reduction is instead supported by the released code, committed
allocation guards, and the current release-pinned replay.

`SHA256SUMS.txt` lists the content hashes for this package's evidence files.
It is not a digital signature or a guarantee about an external collector.
