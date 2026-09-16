# Encryption allocation and timing samples

Windows/amd64, Go 1.27.1, Intel Core Ultra 9 285HX, GOMAXPROCS=8.
The original two benchmarks use 6 x 1s. The payload matrix uses 5 x 200ms.
Before and after use the same harness; the baseline overlay replaces only encryption.go.
These are observed medians, not universal latency or cold-allocation guarantees.

| Benchmark | n per side | ns/op before [range] | ns/op after [range] | B/op before → after | allocs/op before → after |
|---|---:|---:|---:|---:|---:|
| `BenchmarkFieldEncryptor_Encrypt-8` | 6 | 205.3 [205.1–209.7] | 164.85 [164.6–166.1] | 272 → 64 | 6 → 1 |
| `BenchmarkFieldEncryptor_Decrypt-8` | 6 | 93.63 [92.23–95.44] | 80.325 [79.59–80.93] | 80 → 16 | 3 → 1 |
| `BenchmarkEncryptionWorkloads/bytes1/decrypt=false-8` | 5 | 195.7 [194–196.3] | 161.2 [159.4–162.2] | 200 → 48 | 6 → 1 |
| `BenchmarkEncryptionWorkloads/bytes1/decrypt=true-8` | 5 | 76.25 [75.09–77.31] | 68.38 [68.3–70.39] | 33 → 0 | 2 → 0 |
| `BenchmarkEncryptionWorkloads/bytes16/decrypt=false-8` | 5 | 207.6 [205.4–208.7] | 165.4 [165–167.4] | 272 → 64 | 6 → 1 |
| `BenchmarkEncryptionWorkloads/bytes16/decrypt=true-8` | 5 | 93.52 [91.25–94.88] | 81.12 [80.79–82.93] | 80 → 16 | 3 → 1 |
| `BenchmarkEncryptionWorkloads/bytes128/decrypt=false-8` | 5 | 351.8 [351.4–368.7] | 246.4 [245.1–247.7] | 944 → 224 | 6 → 1 |
| `BenchmarkEncryptionWorkloads/bytes128/decrypt=true-8` | 5 | 207 [181.9–243.1] | 150.8 [149.8–151.6] | 416 → 128 | 3 → 1 |
| `BenchmarkEncryptionWorkloads/bytes1024/decrypt=false-8` | 5 | 1306 [1300–1385] | 780.2 [772.2–849.7] | 6416 → 1408 | 6 → 1 |
| `BenchmarkEncryptionWorkloads/bytes1024/decrypt=true-8` | 5 | 1027 [946–1071] | 715.8 [692.7–787.9] | 3200 → 1024 | 3 → 1 |
| `BenchmarkEncryptionWorkloads/bytes65536/decrypt=false-8` | 5 | 61516 [60649–70416] | 49987 [49163–52895] | 409616 → 245761 | 6 → 2 |
| `BenchmarkEncryptionWorkloads/bytes65536/decrypt=true-8` | 5 | 53265 [48781–54505] | 44973 [41697–47308] | 204800 → 139264 | 3 → 2 |

Scratch pooling is limited to 4096 bytes per buffer. Pool misses, including after GC, can allocate. Larger values use the non-pooled fallback. All returned strings own their storage.

Raw files: [original baseline](baseline-bench.txt), [final result](candidate-bench.txt), [payload baseline](payload-baseline.txt), [payload final](payload-candidate.txt). The intermediate two-allocation implementation remains in local scratch, not this evidence bundle.
