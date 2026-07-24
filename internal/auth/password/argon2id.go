// Package password provides argon2id hashing and verification.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memoryKiB   = 64 * 1024 // 64 MiB
	iterations  = 3
	parallelism = 2
	keyLength   = 32
	saltLength  = 16
)

// Hash returns an encoded argon2id string in PHC format:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<base64 salt>$<base64 hash>
func Hash(plain string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: read salt: %w", err)
	}
	hash := argon2.IDKey([]byte(plain), salt, iterations, memoryKiB, parallelism, keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// Verify compares plain against an encoded PHC string. Returns nil on match.
func Verify(plain, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("password: invalid encoded format")
	}
	var version, m, t, p int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return fmt.Errorf("password: parse version: %w", err)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return fmt.Errorf("password: parse params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("password: decode salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("password: decode hash: %w", err)
	}
	got := argon2.IDKey([]byte(plain), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("password: mismatch")
	}
	return nil
}
