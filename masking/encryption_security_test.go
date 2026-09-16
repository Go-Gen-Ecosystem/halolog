package masking

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Different keys, messages, and sizes share the scratch pool, not their data.
// Retain both outputs until all workers finish to expose post-return aliasing.
func TestEncryptionConcurrentKeyIsolation(t *testing.T) {
	const workers, iterations = 8, 96
	var encryptors [workers]*FieldEncryptor
	var ciphertexts, plaintexts [workers][iterations]string
	for worker := range workers {
		key := bytes.Repeat([]byte{byte(worker + 1)}, 32) // synthetic test keys
		var err error
		encryptors[worker], err = NewFieldEncryptorWithKey(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	payload := func(worker, iteration int) string {
		size := [...]int{16, 1024, 4096, 8192}[iteration%4]
		return fmt.Sprintf("worker%d/%d:", worker, iteration) + strings.Repeat("x", size)
	}
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range iterations {
				var err error
				ciphertexts[worker][i], err = encryptors[worker].Encrypt(payload(worker, i))
				if err != nil {
					t.Errorf("worker %d encrypt: %v", worker, err)
					return
				}
				plaintexts[worker][i], err = encryptors[worker].Decrypt(ciphertexts[worker][i])
				if err != nil {
					t.Errorf("worker %d decrypt: %v", worker, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for worker := range workers {
		for i := range iterations {
			want := payload(worker, i)
			got, err := encryptors[worker].Decrypt(ciphertexts[worker][i])
			if err != nil || got != want || plaintexts[worker][i] != want {
				t.Fatalf("worker %d result %d changed: %v", worker, i, err)
			}
			if got, err := encryptors[(worker+1)%workers].Decrypt(ciphertexts[worker][i]); got != "" || !errors.Is(err, ErrDecryptionFailed) {
				t.Fatalf("worker %d result %d accepted another key", worker, i)
			}
		}
	}
}

func TestEncryptionPoolBoundary(t *testing.T) {
	enc, reference := encryptionFixture(t, 32)
	// Encryption needs raw and encoded storage; decryption only needs decoded
	// storage. Exercise both sides of each actual allocation-path boundary.
	firstUnpooled := 1
	for ; ; firstUnpooled++ {
		_, size, ok := encryptionBufferSize(firstUnpooled, 12, 16, 4)
		if !ok {
			t.Fatal("unexpected size overflow")
		}
		if size > encryptionScratchCapacity {
			break
		}
	}
	for _, size := range []int{firstUnpooled - 1, firstUnpooled, 4066, 4067, 4068, 4069} {
		t.Run(fmt.Sprintf("bytes%d", size), func(t *testing.T) {
			plain := strings.Repeat("s", size)
			ciphertext, err := enc.Encrypt(plain)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := referenceFieldDecrypt(reference, "enc:", ciphertext); err != nil || got != plain {
				t.Fatal("encryption changed at the pool boundary")
			}
			if got, err := enc.Decrypt(ciphertext); err != nil || got != plain {
				t.Fatal("decryption changed at the pool boundary")
			}
			raw, err := base64.StdEncoding.DecodeString(ciphertext[4:])
			if err != nil {
				t.Fatal(err)
			}
			raw[len(raw)-1] ^= 1
			if got, err := enc.Decrypt("enc:" + base64.StdEncoding.EncodeToString(raw)); got != "" || !errors.Is(err, ErrDecryptionFailed) {
				t.Fatal("authentication failure exposed plaintext at the pool boundary")
			}
		})
	}
}

func TestEncryptionPrefixIsNotAuthentication(t *testing.T) {
	enc, _ := encryptionFixture(t, 32)
	if !enc.IsEncrypted("enc:not-authenticated") {
		t.Fatal("IsEncrypted no longer implements its prefix-only contract")
	}
	if got, err := enc.Decrypt("enc:not-authenticated"); got != "" || err == nil {
		t.Fatal("a matching prefix bypassed authentication")
	}
	// The existing wire format authenticates the value, not its field name or
	// display prefix. Pin that limit instead of implying tenant/context binding.
	ciphertext, err := enc.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	enc.SetPrefix("other:")
	if got, err := enc.Decrypt("other:" + ciphertext[4:]); err != nil || got != "secret" {
		t.Fatal("unexpected change to the existing unauthenticated-prefix format")
	}
}
