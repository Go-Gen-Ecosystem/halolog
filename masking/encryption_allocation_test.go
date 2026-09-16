package masking

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var encryptionResultSink string

type failedEncryptionEntropy struct{}

func (failedEncryptionEntropy) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestEncryptionEntropyFailure(t *testing.T) {
	enc, _ := encryptionFixture(t, 32)
	// This test is deliberately not parallel: crypto/rand.Reader is global.
	original := rand.Reader
	rand.Reader = failedEncryptionEntropy{}
	defer func() { rand.Reader = original }()
	if value, err := enc.Encrypt("sensitive-value"); value != "" || !errors.Is(err, ErrEncryptionFailed) {
		t.Fatalf("entropy failure returned len=%d err=%v", len(value), err)
	}
}

func TestEncryptionBufferSize(t *testing.T) {
	const maxInt = int(^uint(0) >> 1)
	for _, tc := range []struct {
		plain, nonce, overhead, prefix, raw, total int
		ok                                         bool
	}{
		{16, 12, 16, 4, 44, 108, true},
		{0, 12, 16, 4, 28, 72, true},
		{0, 0, 0, 0, 0, 0, true},
		{-1, 12, 16, 4, 0, 0, false},
		{16, -1, 16, 4, 0, 0, false},
		{16, 12, -1, 4, 0, 0, false},
		{16, 12, 16, -1, 0, 0, false},
		{1, maxInt, 16, 4, 0, 0, false},
		{maxInt, 12, 16, 4, 0, 0, false},
		{maxInt - 2, 0, 0, 0, 0, 0, false},
		{16, 12, 16, maxInt, 0, 0, false},
		{maxInt / 2, 0, 0, 0, 0, 0, false},
	} {
		raw, total, ok := encryptionBufferSize(tc.plain, tc.nonce, tc.overhead, tc.prefix)
		if raw != tc.raw || total != tc.total || ok != tc.ok {
			t.Fatalf("sizes(%d,%d,%d,%d)=(%d,%d,%v), want(%d,%d,%v)", tc.plain, tc.nonce, tc.overhead, tc.prefix, raw, total, ok, tc.raw, tc.total, tc.ok)
		}
	}
}

// Independent layout oracle: prefix + Base64(nonce || ciphertext || GCM tag).
// It deliberately retains the original allocating Seal/Open paths.
func referenceFieldEncrypt(aead cipher.AEAD, prefix, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", ErrEncryptionFailed
	}
	raw := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(raw), nil
}

func referenceFieldDecrypt(aead cipher.AEAD, prefix, ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if len(ciphertext) <= len(prefix) || !strings.HasPrefix(ciphertext, prefix) {
		return "", ErrInvalidCiphertext
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext[len(prefix):])
	if err != nil || len(raw) < aead.NonceSize() {
		return "", ErrInvalidCiphertext
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", ErrDecryptionFailed
	}
	return string(plain), nil
}

func encryptionFixture(t testing.TB, keySize int) (*FieldEncryptor, cipher.AEAD) {
	t.Helper()
	key := make([]byte, keySize)
	for i := range key {
		key[i] = byte(i*7 + 3) // reproducible test key, never a production key
	}
	enc, err := NewFieldEncryptorWithKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return enc, aead
}

func TestEncryptionWireCompatibility(t *testing.T) {
	for _, keySize := range []int{16, 24, 32} {
		enc, reference := encryptionFixture(t, keySize)
		for _, prefix := range []string{"enc:", "", "encrypted:", "🔐:"} {
			enc.SetPrefix(prefix)
			for _, size := range []int{0, 1, 15, 16, 17, 128, 512, 4096, 65536} {
				t.Run(fmt.Sprintf("key%d/prefix%d/bytes%d", keySize, len(prefix), size), func(t *testing.T) {
					payload := make([]byte, size)
					for i := range payload {
						payload[i] = byte(i*13 + 255) // includes NUL and invalid UTF-8
					}
					plain := string(payload)
					original, err := referenceFieldEncrypt(reference, prefix, plain)
					if err != nil {
						t.Fatal(err)
					}
					if got, err := enc.Decrypt(original); err != nil || got != plain {
						t.Fatalf("old -> new: err=%v, plaintext matches=%v", err, got == plain)
					}
					current, err := enc.Encrypt(plain)
					if err != nil {
						t.Fatal(err)
					}
					if got, err := referenceFieldDecrypt(reference, prefix, current); err != nil || got != plain {
						t.Fatalf("new -> old: err=%v, plaintext matches=%v", err, got == plain)
					}
					if size > 0 {
						raw, err := base64.StdEncoding.DecodeString(current[len(prefix):])
						if err != nil || len(raw) != reference.NonceSize()+size+reference.Overhead() {
							t.Fatalf("wire layout length=%d, err=%v", len(raw), err)
						}
						// Given the emitted nonce, the ciphertext and tag must be exact.
						want := reference.Seal(nil, raw[:reference.NonceSize()], payload, nil)
						if !bytes.Equal(raw[reference.NonceSize():], want) {
							t.Fatal("ciphertext or authentication tag differs from the reference")
						}
					}
				})
			}
		}
	}
}

func TestEncryptionRejectsTamperingWithoutPlaintext(t *testing.T) {
	enc, reference := encryptionFixture(t, 32)
	ciphertext, err := enc.Encrypt("sensitive-value-123")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext[4:])
	if err != nil {
		t.Fatal(err)
	}
	// Mutate every nonce, ciphertext and tag byte independently.
	for i := range raw {
		raw[i] ^= 1
		got, err := enc.Decrypt("enc:" + base64.StdEncoding.EncodeToString(raw))
		if !errors.Is(err, ErrDecryptionFailed) || got != "" {
			t.Fatalf("tampered byte %d: plaintext length=%d err=%v", i, len(got), err)
		}
		raw[i] ^= 1
	}
	for _, input := range []string{"", "enc:", "bad:abc", "enc:!", "enc:AA==", "enc:" + base64.StdEncoding.EncodeToString(raw[:reference.NonceSize()]), "enc:\n"} {
		want, wantErr := referenceFieldDecrypt(reference, "enc:", input)
		got, err := enc.Decrypt(input)
		if got != want || !errors.Is(err, wantErr) {
			t.Fatalf("malformed %q: got %q/%v, want %q/%v", input, got, err, want, wantErr)
		}
	}
}

func TestEncryptionOwnedResults(t *testing.T) {
	enc, reference := encryptionFixture(t, 32)
	var ciphertexts, plaintexts [256]string
	for i := range ciphertexts {
		want := "retained-value-" + strconv.Itoa(i)
		var err error
		ciphertexts[i], err = enc.Encrypt(want)
		if err != nil {
			t.Fatal(err)
		}
		plaintexts[i], err = enc.Decrypt(ciphertexts[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range ciphertexts {
		want := "retained-value-" + strconv.Itoa(i)
		if plaintexts[i] != want {
			t.Fatalf("retained plaintext %d changed", i)
		}
		if got, err := referenceFieldDecrypt(reference, "enc:", ciphertexts[i]); err != nil || got != want {
			t.Fatalf("retained ciphertext %d changed", i)
		}
	}
}

func TestEncryptionPrefixConcurrentAccess(t *testing.T) {
	enc, _ := encryptionFixture(t, 32)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 10000 {
				if worker == 0 {
					enc.SetPrefix([]string{"enc:", "encrypted:", ""}[i%3])
				} else {
					_ = enc.IsEncrypted("enc:payload")
					_ = enc.IsEncrypted("")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestEncryptionScratchClearedBeforeReuse(t *testing.T) {
	for _, size := range []int{1, 16, 256, encryptionScratchCapacity, encryptionScratchCapacity + 1} {
		buffer, owner := acquireEncryptionScratch(size)
		if (owner != nil) != (size <= encryptionScratchCapacity) || len(buffer) != size || cap(buffer) != size {
			t.Fatalf("unexpected scratch ownership for size %d", size)
		}
		for i := range buffer {
			buffer[i] = byte(i%255 + 1)
		}
		releaseEncryptionScratch(buffer, owner)
		// Test-only inspection: no other operation borrows from the pool here.
		for _, value := range buffer {
			if value != 0 {
				t.Fatalf("size %d retained temporary data after release", size)
			}
		}
	}
}

type overwriteOpenAEAD struct {
	cipher.AEAD
	written []byte
	fail    bool
}

func (a *overwriteOpenAEAD) Open(dst, _, _, _ []byte) ([]byte, error) {
	a.written = dst[:cap(dst)]
	for i := range a.written {
		a.written[i] = 'S'
	}
	if a.fail {
		return nil, ErrDecryptionFailed
	}
	return a.written[:4], nil
}

func TestEncryptionOpenScratchLifetime(t *testing.T) {
	for _, fail := range []bool{false, true} {
		enc, reference := encryptionFixture(t, 32)
		probe := &overwriteOpenAEAD{AEAD: reference, fail: fail}
		enc.gcm = probe
		input := "enc:" + base64.StdEncoding.EncodeToString(make([]byte, 44))
		result, err := enc.Decrypt(input)
		if fail {
			if !errors.Is(err, ErrDecryptionFailed) || result != "" {
				t.Fatal("authentication failure exposed plaintext")
			}
		} else if err != nil || result != "SSSS" {
			t.Fatal("owned result was not copied before scratch release")
		}
		// Open is allowed to overwrite all dst capacity even on failure. Verify
		// release scrubs that capacity, not just the successful plaintext length.
		for _, value := range probe.written {
			if value != 0 {
				t.Fatal("Open's temporary plaintext survived release")
			}
		}
	}
}

func TestEncryptionResultsAcrossPoolGC(t *testing.T) {
	enc, reference := encryptionFixture(t, 32)
	var ciphertexts, plaintexts [64]string
	for i := range ciphertexts {
		if i%8 == 0 {
			runtime.GC()
			runtime.GC() // pool contents may be discarded; this is not an allocation assertion
		}
		want := "survives-gc-" + strconv.Itoa(i)
		var err error
		ciphertexts[i], err = enc.Encrypt(want)
		if err != nil {
			t.Fatal(err)
		}
		plaintexts[i], err = enc.Decrypt(ciphertexts[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range ciphertexts {
		want := "survives-gc-" + strconv.Itoa(i)
		if got, err := referenceFieldDecrypt(reference, "enc:", ciphertexts[i]); err != nil || got != want || plaintexts[i] != want {
			t.Fatalf("retained result %d changed across GC", i)
		}
	}
}

func FuzzEncryptionCompatibility(f *testing.F) {
	for _, plain := range []string{"", "user@example.com", "\x00\xff\xc0", strings.Repeat("a", 257)} {
		f.Add(plain, "enc:")
	}
	enc, reference := encryptionFixture(f, 32)
	f.Fuzz(func(t *testing.T, plain, prefix string) {
		if len(plain) > 65536 || len(prefix) > 256 {
			t.Skip()
		}
		enc.SetPrefix(prefix)
		old, err := referenceFieldEncrypt(reference, prefix, plain)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := enc.Decrypt(old); err != nil || got != plain {
			t.Fatalf("old -> new: err=%v, plaintext matches=%v", err, got == plain)
		}
		current, err := enc.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := referenceFieldDecrypt(reference, prefix, current); err != nil || got != plain {
			t.Fatalf("new -> old: err=%v, plaintext matches=%v", err, got == plain)
		}
		if got, err := enc.Decrypt(current); err != nil || got != plain {
			t.Fatal("roundtrip failed")
		}
	})
}

func FuzzEncryptionMalformedInput(f *testing.F) {
	enc, reference := encryptionFixture(f, 32)
	valid, err := enc.Encrypt("secret")
	if err != nil {
		f.Fatal(err)
	}
	for _, input := range []string{"", "enc:", "enc:AA==", valid, valid + "!", "enc:\x00\xff"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 65536 {
			t.Skip()
		}
		want, wantErr := referenceFieldDecrypt(reference, "enc:", input)
		got, err := enc.Decrypt(input)
		if got != want || !errors.Is(err, wantErr) {
			t.Fatalf("decryption contract differs: len=%d err=%v wantErr=%v", len(input), err, wantErr)
		}
	})
}

func BenchmarkEncryptionWorkloads(b *testing.B) {
	enc, _ := encryptionFixture(b, 32)
	for _, size := range []int{1, 16, 128, 1024, 65536} {
		plain := strings.Repeat("x", size)
		ciphertext, err := enc.Encrypt(plain)
		if err != nil {
			b.Fatal(err)
		}
		for _, decrypt := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes%d/decrypt=%v", size, decrypt), func(b *testing.B) {
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if decrypt {
						encryptionResultSink, err = enc.Decrypt(ciphertext)
					} else {
						encryptionResultSink, err = enc.Encrypt(plain)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if decrypt && encryptionResultSink != plain {
					b.Fatal("plaintext changed")
				}
			})
		}
	}
}
