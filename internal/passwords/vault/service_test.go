package vault

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestValidateEnvelopeAppliesDefaultAlgorithm covers the A1 regression: the
// function takes a pointer so the "argon2id" default actually sticks on the
// stored value instead of mutating a value-copy.
func TestValidateEnvelopeAppliesDefaultAlgorithm(t *testing.T) {
	e := KeyEnvelope{
		UserID: "u", KDFSalt: b64bytes(16, 1),
		KDFMemoryKiB: 1024, KDFIterations: 1, KDFParallelism: 1,
		ProtectedVaultKey: cipherFixture(48, 2), ProtectedVaultNonce: nonceFixture(3),
	}
	if err := validateEnvelope(&e); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if e.KDFAlgorithm != "argon2id" {
		t.Fatalf("KDFAlgorithm = %q, want argon2id (the default)", e.KDFAlgorithm)
	}
}

func TestValidateEnvelopeRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		env  KeyEnvelope
	}{
		{"missing user", KeyEnvelope{}},
		{"missing salt", KeyEnvelope{UserID: "u"}},
		{"missing protected key", KeyEnvelope{UserID: "u", KDFSalt: "s"}},
		{"zero memory", KeyEnvelope{UserID: "u", KDFSalt: b64bytes(16, 1), ProtectedVaultKey: cipherFixture(48, 2), ProtectedVaultNonce: nonceFixture(3)}},
		{"bad nonce", KeyEnvelope{UserID: "u", KDFSalt: b64bytes(16, 1), KDFMemoryKiB: 1024, KDFIterations: 1, KDFParallelism: 1, ProtectedVaultKey: cipherFixture(48, 2), ProtectedVaultNonce: "not-base64"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if err := validateEnvelope(&c.env); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("validate = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// TestValidateEnvelopeRejectsExcessiveKDF covers the upper-bound guard added
// so a malicious client cannot register an envelope whose unlock pins gigabytes.
func TestValidateEnvelopeRejectsExcessiveKDF(t *testing.T) {
	e := KeyEnvelope{
		UserID: "u", KDFSalt: b64bytes(16, 1),
		KDFMemoryKiB: 2 << 20, KDFIterations: 3, KDFParallelism: 2,
		ProtectedVaultKey: cipherFixture(48, 2), ProtectedVaultNonce: nonceFixture(3),
	}
	if err := validateEnvelope(&e); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validate = %v, want ErrInvalidInput", err)
	}
}

// TestCreateAttachmentSizeLimit covers the A6 fix: an over-cap attachment is
// rejected before it reaches the repository.
func TestCreateAttachmentSizeLimit(t *testing.T) {
	repo := testRepo(t)
	svc := NewService(repo)
	ctx := context.Background()
	_, err := svc.CreateAttachment(ctx, "u", "item", "path",
		MaxAttachmentBytes+1, cipherFixture(48, 50), nonceFixture(60), cipherFixture(24, 70), nonceFixture(80))
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized attachment = %v, want ErrInvalidInput/too large", err)
	}
	// Negative size is still rejected.
	_, err = svc.CreateAttachment(ctx, "u", "item", "path",
		-1, cipherFixture(48, 50), nonceFixture(60), cipherFixture(24, 70), nonceFixture(80))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative size = %v, want ErrInvalidInput", err)
	}
}

func TestValidateItemRejectsMalformedCiphertext(t *testing.T) {
	if err := validateItem(ItemInput{
		Type: ItemLogin, NameCipher: "not-base64", NameNonce: nonceFixture(1),
		DataCipher: cipherFixture(32, 2), DataNonce: nonceFixture(3),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad name cipher = %v, want ErrInvalidInput", err)
	}
	if err := validateItem(ItemInput{
		Type: ItemLogin, NameCipher: cipherFixture(24, 1), NameNonce: b64bytes(8, 2),
		DataCipher: cipherFixture(32, 3), DataNonce: nonceFixture(4),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad nonce length = %v, want ErrInvalidInput", err)
	}
}

func TestRotateEnvelopeRequiresIfVersion(t *testing.T) {
	repo := testRepo(t)
	svc := NewService(repo)
	ctx := context.Background()
	if err := svc.Setup(ctx, env("u1")); err != nil {
		t.Fatal(err)
	}
	// ifVersion 0 must be rejected as invalid input, not silently applied.
	if err := svc.RotateEnvelope(ctx, env("u"), 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rotate ifVersion=0 = %v, want ErrInvalidInput", err)
	}
}
