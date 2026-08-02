package crypto

import (
	"strings"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	s, err := New([]byte("session-secret-32-bytes-long-xxxx"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plain := "sk-abc-123"
	ct, nonce, err := s.EncryptString(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ct == "" || nonce == "" {
		t.Fatal("empty cipher/nonce")
	}
	got, err := s.DecryptString(ct, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("round-trip mismatch: %q != %q", got, plain)
	}
}

func TestSealTamperFails(t *testing.T) {
	s, _ := New([]byte("session-secret-32-bytes-long-xxxx"))
	ct, nonce, _ := s.EncryptString("hello")
	// Flip a bit in the ciphertext base64.
	tampered := ct[:len(ct)-1]
	if strings.HasSuffix(ct, "A") {
		tampered = ct[:len(ct)-1] + "B"
	} else {
		tampered = ct[:len(ct)-1] + "A"
	}
	if _, err := s.DecryptString(tampered, nonce); err == nil {
		t.Fatal("expected decrypt error on tampered ciphertext")
	}
}

func TestSealEmptyInput(t *testing.T) {
	s, _ := New([]byte("session-secret-32-bytes-long-xxxx"))
	got, err := s.DecryptString("", "")
	if err != nil {
		t.Fatalf("empty decrypt: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty plaintext, got %q", got)
	}
}

func TestSealKeySensitivity(t *testing.T) {
	a, _ := New([]byte("key-one-32-bytes-long-xxxxxxxxx"))
	b, _ := New([]byte("key-two-32-bytes-long-xxxxxxxxx"))
	ct, nonce, _ := a.EncryptString("secret")
	if _, err := b.DecryptString(ct, nonce); err == nil {
		t.Fatal("expected decrypt failure under a different key")
	}
}
