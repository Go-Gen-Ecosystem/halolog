package masking

import (
	"encoding/base64"
	"sync"
)

// Pool only bounded scratch, not keys, input strings, or result strings. The
// runtime may discard pooled items at a GC; allocation guarantees are therefore
// steady-state budgets, not promises for cold calls or arbitrary field sizes.
const encryptionScratchCapacity = 4096

type encryptionScratch struct {
	data [encryptionScratchCapacity]byte
}

var encryptionScratchPool = sync.Pool{
	New: func() any { return new(encryptionScratch) },
}

func acquireEncryptionScratch(size int) ([]byte, *encryptionScratch) {
	if size > encryptionScratchCapacity {
		return make([]byte, size), nil
	}
	owner := encryptionScratchPool.Get().(*encryptionScratch)
	return owner.data[:size:size], owner
}

func releaseEncryptionScratch(data []byte, owner *encryptionScratch) {
	// Scrub every potentially written byte before publishing the buffer for
	// reuse, including Open's destination when authentication failed.
	clear(data)
	if owner != nil {
		encryptionScratchPool.Put(owner)
	}
}

// encryptionBufferSize bounds the scratch layout before adding lengths or
// calling EncodedLen, whose arithmetic assumes representable input sizes.
func encryptionBufferSize(plain, nonce, overhead, prefix int) (raw, total int, ok bool) {
	const maxInt = int(^uint(0) >> 1)
	if plain < 0 || nonce < 0 || overhead < 0 || prefix < 0 {
		return 0, 0, false
	}
	if nonce > maxInt-overhead || plain > maxInt-nonce-overhead {
		return 0, 0, false
	}
	raw = nonce + plain + overhead
	if raw > (maxInt/4)*3 {
		return 0, 0, false
	}
	encoded := base64.StdEncoding.EncodedLen(raw)
	if prefix > maxInt-encoded {
		return 0, 0, false
	}
	output := prefix + encoded
	if raw > maxInt-output {
		return 0, 0, false
	}
	return raw, raw + output, true
}
