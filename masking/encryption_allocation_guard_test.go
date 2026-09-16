//go:build !race

package masking

import (
	"fmt"
	"strings"
	"testing"
)

func TestEncryptionAllocationBudget(t *testing.T) {
	enc, _ := encryptionFixture(t, 32)
	for _, size := range []int{1, 16, 128, 1024, 65536} {
		plain := strings.Repeat("x", size)
		ciphertext, err := enc.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		for _, decrypt := range []bool{false, true} {
			t.Run(fmt.Sprintf("bytes%d/decrypt=%v", size, decrypt), func(t *testing.T) {
				allocs := testing.AllocsPerRun(100, func() {
					if decrypt {
						encryptionResultSink, err = enc.Decrypt(ciphertext)
					} else {
						encryptionResultSink, err = enc.Encrypt(plain)
					}
				})
				budget := float64(1)
				if size == 65536 {
					budget = 2 // large values deliberately bypass bounded scratch pooling
				}
				if err != nil || allocs > budget {
					t.Fatalf("allocs=%v budget=%v err=%v", allocs, budget, err)
				}
				if decrypt && encryptionResultSink != plain {
					t.Fatal("plaintext changed")
				}
			})
		}
	}
}
