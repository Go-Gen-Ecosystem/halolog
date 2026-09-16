// Copyright 2025 Admilson B. F. Cossa
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package masking provides PII masking and field encryption
// Author: Admilson B. F. Cossa

package masking

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"sync"
)

var (
	// ErrInvalidKeyLength indicates the encryption key is not the correct length
	ErrInvalidKeyLength = errors.New("encryption key must be 16, 24, or 32 bytes for AES-128, AES-192, or AES-256")
	// ErrEncryptionFailed indicates encryption operation failed
	ErrEncryptionFailed = errors.New("encryption failed")
	// ErrDecryptionFailed indicates decryption operation failed
	ErrDecryptionFailed = errors.New("decryption failed")
	// ErrInvalidCiphertext indicates the ciphertext is malformed
	ErrInvalidCiphertext = errors.New("invalid ciphertext")
)

// FieldEncryptor provides AES-GCM field-level encryption.
// Thread-safe for concurrent use
type FieldEncryptor struct {
	mu     sync.RWMutex
	key    []byte
	gcm    cipher.AEAD
	prefix string // Prefix for encrypted values (default: "enc:")
}

// NewFieldEncryptor creates a new AES-256 field encryptor
// The key can be any length - it will be hashed to 32 bytes using SHA-256
// Hashing does not add entropy or provide password stretching: use a high-entropy
// secret. Existing key derivation is retained for ciphertext compatibility.
func NewFieldEncryptor(key string) (*FieldEncryptor, error) {
	if key == "" {
		return nil, ErrInvalidKeyLength
	}

	// Hash the key to ensure 32 bytes for AES-256
	hash := sha256.Sum256([]byte(key))
	keyBytes := hash[:]

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &FieldEncryptor{
		key:    keyBytes,
		gcm:    gcm,
		prefix: "enc:",
	}, nil
}

// NewFieldEncryptorWithKey creates an AES-GCM encryptor from a raw key.
//
// The key MUST be exactly 16, 24, or 32 bytes and is used verbatim: a 16-byte
// key yields AES-128-GCM, 24 bytes AES-192-GCM, and 32 bytes AES-256-GCM. The
// key material is never zero-padded — doing so would silently downgrade the
// effective entropy and misrepresent the AES variant, so any other length is
// rejected with ErrInvalidKeyLength. NewFieldEncryptor retains a SHA-256-based
// string-key compatibility path; it is not a password-stretching function.
func NewFieldEncryptorWithKey(key []byte) (*FieldEncryptor, error) {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return nil, ErrInvalidKeyLength
	}

	// Copy the key verbatim (defensive copy; no padding). AES selects the
	// variant (128/192/256) from the actual key length.
	keyBytes := make([]byte, len(key))
	copy(keyBytes, key)

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &FieldEncryptor{
		key:    keyBytes,
		gcm:    gcm,
		prefix: "enc:",
	}, nil
}

// Encrypt encrypts plaintext using AES-GCM with the constructor's key size.
// It returns an independently owned prefix + Base64(nonce || ciphertext || tag).
// Small calls may reuse scrubbed scratch; cold calls and larger values allocate
// additional scratch. No returned string aliases reusable memory.
func (e *FieldEncryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Scratch holds both the raw record and its Base64 representation. Small
	// operations borrow a private, scrubbed buffer; the returned string always
	// owns its storage and is never an alias into reusable scratch.
	nonceSize := e.gcm.NonceSize()
	rawSize, totalSize, ok := encryptionBufferSize(len(plaintext), nonceSize, e.gcm.Overhead(), len(e.prefix))
	if !ok {
		return "", ErrEncryptionFailed
	}
	scratch, owner := acquireEncryptionScratch(totalSize)
	if owner != nil {
		defer releaseEncryptionScratch(scratch, owner)
	}
	raw := scratch[:rawSize:rawSize]
	nonce := raw[:nonceSize:nonceSize]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", ErrEncryptionFailed
	}

	// AEAD explicitly permits plaintext[:0] as dst. Reserve the tag capacity
	// while keeping the nonce and encoded-output regions outside that slice.
	plain := raw[nonceSize : nonceSize+len(plaintext)]
	copy(plain, plaintext)
	sealed := e.gcm.Seal(plain[:0], nonce, plain, nil)

	encoded := scratch[rawSize:]
	copy(encoded, e.prefix)
	base64.StdEncoding.Encode(encoded[len(e.prefix):], raw[:nonceSize+len(sealed)])
	return string(encoded), nil
}

// Decrypt decrypts a base64-encoded ciphertext
// Expects the value to have the encryption prefix
func (e *FieldEncryptor) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Check and remove prefix
	if len(ciphertext) <= len(e.prefix) {
		return "", ErrInvalidCiphertext
	}
	if ciphertext[:len(e.prefix)] != e.prefix {
		return "", ErrInvalidCiphertext
	}
	encoded := ciphertext[len(e.prefix):]

	// Decode from base64
	data, owner := acquireEncryptionScratch(base64.StdEncoding.DecodedLen(len(encoded)))
	defer releaseEncryptionScratch(data, owner)
	n, err := base64.StdEncoding.Decode(data, []byte(encoded))
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	data = data[:n]

	// Extract nonce and ciphertext
	nonceSize := e.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", ErrInvalidCiphertext
	}

	nonce, encryptedData := data[:nonceSize], data[nonceSize:]

	// Reuse only this call's decoded ciphertext buffer. Open's exact-overlap
	// contract permits this; the nonce is outside the writable destination.
	plaintext, err := e.gcm.Open(encryptedData[:0], nonce, encryptedData, nil)
	if err != nil {
		return "", ErrDecryptionFailed
	}

	return string(plaintext), nil // the deferred release scrubs scratch after this copy
}

// IsEncrypted checks only the prefix, not authenticity. Use Decrypt to verify
// the authentication tag; this predicate is not a security boundary.
func (e *FieldEncryptor) IsEncrypted(value string) bool {
	e.mu.RLock()
	prefix := e.prefix
	e.mu.RUnlock()
	return len(value) > len(prefix) && value[:len(prefix)] == prefix
}

// SetPrefix sets a custom prefix for encrypted values
func (e *FieldEncryptor) SetPrefix(prefix string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prefix = prefix
}

// GlobalFieldEncryptor is the singleton encryptor for system-wide use.
// Initialize it before starting concurrent consumers. Replacing this exported
// variable is not synchronized and is not a supported concurrent rotation API.
var GlobalFieldEncryptor *FieldEncryptor

// InitGlobalEncryptor initializes the global field encryptor during startup,
// before concurrent consumers are started.
func InitGlobalEncryptor(key string) error {
	encryptor, err := NewFieldEncryptor(key)
	if err != nil {
		return err
	}
	GlobalFieldEncryptor = encryptor
	return nil
}
