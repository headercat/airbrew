// Package crypto provides the AES-256-GCM seal used to encrypt provider API
// keys at rest. The key is derived once at startup from the session HMAC
// secret so a single env knob (AIRBREW_SESSION_SECRET) unlocks both cookies
// and provider configs.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Seal is an AES-256-GCM encryptor anchored on a fixed key.
type Seal struct {
	aead cipher.AEAD
}

// New derives an AES-256-GCM cipher from keyMaterial using SHA-256. The
// input is treated as keying material, not as a literal AES key, so any
// length is acceptable.
func New(keyMaterial []byte) (*Seal, error) {
	sum := sha256.Sum256(keyMaterial)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: aes new: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm new: %w", err)
	}
	return &Seal{aead: aead}, nil
}

// Encrypt returns the base64-encoded ciphertext and nonce. The nonce is
// freshly generated for each call.
func (s *Seal) Encrypt(plaintext []byte) (cipherB64, nonceB64 string, err error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", fmt.Errorf("crypto: rand nonce: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct),
		base64.StdEncoding.EncodeToString(nonce), nil
}

// Decrypt reverses Encrypt. An empty input pair yields an empty output
// without error so callers can decode "no key set" rows cleanly.
func (s *Seal) Decrypt(cipherB64, nonceB64 string) ([]byte, error) {
	if cipherB64 == "" && nonceB64 == "" {
		return nil, nil
	}
	if cipherB64 == "" || nonceB64 == "" {
		return nil, errors.New("crypto: cipher and nonce must both be set or both empty")
	}
	ct, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("crypto: decode ciphertext: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return nil, fmt.Errorf("crypto: decode nonce: %w", err)
	}
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return pt, nil
}

// EncryptString is the string-typed shortcut over Encrypt.
func (s *Seal) EncryptString(plain string) (string, string, error) {
	return s.Encrypt([]byte(plain))
}

// DecryptString is the string-typed shortcut over Decrypt.
func (s *Seal) DecryptString(cipherB64, nonceB64 string) (string, error) {
	pt, err := s.Decrypt(cipherB64, nonceB64)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
