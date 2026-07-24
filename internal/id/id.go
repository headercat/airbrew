// Package id generates application-level unique identifiers (nanoid-like).
//
// The alphabet is URL-safe and contains 64 symbols; each byte from crypto/rand
// is masked to 6 bits to index it (256 / 64 == 4, no modular bias).
package id

import (
	"crypto/rand"
)

const alphabet = "_-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const size = 21

// New returns a fresh 21-character identifier.
func New() string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is unrecoverable.
		panic("id: rand.Read failed: " + err.Error())
	}
	for i := range b {
		b[i] = alphabet[b[i]&63]
	}
	return string(b)
}
