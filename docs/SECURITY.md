# Security

## Reporting a vulnerability

Please use the repository's **Security → Report a vulnerability** flow to
report suspected vulnerabilities privately. Do not open a public issue with
vulnerability details. Include a minimal reproduction; you will receive an
acknowledgment and a fix timeline. Please allow a reasonable disclosure window
before publishing details.

## Security-relevant features (implemented and tested)

- **PII masking** (`masking`): rule- and pattern-based masking applied to
  entries before any adapter sees them. **Design invariant, enforced in
  code and pinned by tests:** configuring a masker disables the
  direct-append fast path, so no optimization can ever bypass masking.
- **Sensitive-field registry** (`registry`): O(1) name/ID lookup over a
  pre-seeded set of sensitive field names, with pattern and heuristic
  fallbacks; query-path counters are atomic (race-detector clean).
- **Field-level encryption** (`masking/encryption.go`): AES-GCM field
  encryption. The raw-key constructor accepts exactly 16, 24, or 32 bytes
  without padding. The string-key constructor instead hashes a nonempty
  secret with SHA-256; it is not a password-stretching function.
- **File output hardening** (`adapters/outputs/file`): log files, rotated
  backups, compressed archives, and lock/PID files are created owner-only
  (0600); the cross-process file lock never treats a live process's lock as
  stale (probe via signal 0 / process handle, EPERM counts as alive).
- **JSON output robustness**: control characters, quotes, and backslashes
  are always escaped (the SWAR scanner is verified byte-identical to the
  reference scanner over every byte value in every position); non-finite
  floats render as quoted strings so a hostile value cannot corrupt the log
  stream for downstream parsers.
- **Misuse containment**: reusing a fluent builder after its terminal call
  is a guarded no-op (generation counter), not memory corruption or
  cross-request field leakage; pooled entries clear their used field values
  on release so recycled buffers never retain a previous line's data.

## Scope notes

### Field encryption in production

- Use high-entropy keys from your secret-management system. The string-key
  compatibility constructor does not make a weak password strong. Prefer the
  raw-key constructor for explicitly managed AES keys.
- Each nonempty encryption obtains a fresh 96-bit nonce from `crypto/rand`.
  Rotate keys before reaching 2^32 encrypted values per key, counting all
  fields, processes, instances, and restarts that share it. This follows
  [Go's random-nonce GCM guidance](https://pkg.go.dev/crypto/cipher#NewGCM).
  HaloLog does not enforce a distributed usage budget or supply a keyring.
  A lower operational limit may be appropriate for your threat model.
- The existing format is `prefix + Base64(nonce || ciphertext || tag)`.
  There is no key identifier, associated data, tenant/field binding, expiry,
  or replay protection. A valid value remains decryptable if moved to another
  field using the same key. `IsEncrypted` checks a prefix only; it is not an
  authenticity check. Empty strings pass through without encryption.
- Configure the prefix before use. Individual methods are synchronized, but
  changing a prefix between Encrypt and Decrypt can invalidate the latter's
  prefix check. Initialize `GlobalFieldEncryptor` before concurrent consumers;
  replacing the exported global is not a synchronized rotation mechanism.
- Check returned errors. Never fall back to logging the original plaintext
  when encryption fails. Bound field sizes and reject oversized untrusted
  ciphertext before calling Decrypt: the API allocates scratch proportional
  to input size and does not impose an application-level size limit.
- Pooled scratch is exclusive to one call and cleared before reuse. Decrypt
  clears temporary storage on both authentication success and failure, before
  returning it to the pool. Returned strings own their storage. This is not a
  guarantee of complete process-memory erasure: caller strings, returned
  plaintext, key schedules, and runtime copies have different lifetimes.
- The pool holds buffers of at most 4 KiB each, not a hard global memory cap.
  Cold calls, pool misses after GC, and larger values can allocate additional
  scratch. One-allocation measurements are scoped steady-state results, not
  a universal security or performance guarantee.

The optimization and its compatibility tests do not constitute an independent
cryptographic audit or FIPS certification. The deployment must still meet its
own key-management, access-control, retention, and incident-response policies.

### Other boundaries

- The HTTP adapter posts batches to the URL you configure; supplying
  credentials via headers and using TLS endpoints is the caller's
  responsibility.
- Sampling never drops FATAL/PANIC lines.
- This library does not itself provide tamper-evident (hash-chained) audit
  logs; if a document claims otherwise it is a draft, not shipped behavior.
