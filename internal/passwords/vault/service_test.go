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
		UserID: "u", KDFSalt: "s",
		KDFMemoryKiB: 1024, KDFIterations: 1, KDFParallelism: 1,
		ProtectedVaultKey: "k", ProtectedVaultNonce: "n",
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
		{"zero memory", KeyEnvelope{UserID: "u", KDFSalt: "s", ProtectedVaultKey: "k", ProtectedVaultNonce: "n"}},
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
		UserID: "u", KDFSalt: "s",
		KDFMemoryKiB: 2 << 20, KDFIterations: 3, KDFParallelism: 2,
		ProtectedVaultKey: "k", ProtectedVaultNonce: "n",
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
		MaxAttachmentBytes+1, "fk", "fkn", "nm", "nmn")
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized attachment = %v, want ErrInvalidInput/too large", err)
	}
	// Negative size is still rejected.
	_, err = svc.CreateAttachment(ctx, "u", "item", "path",
		-1, "fk", "fkn", "nm", "nmn")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative size = %v, want ErrInvalidInput", err)
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
