package auth_test

import (
	"testing"

	"github.com/Haruko386/GoBridge/internal/auth"
	"github.com/Haruko386/GoBridge/internal/identity"
)

func TestPersistentIdentityCanAuthenticate(t *testing.T) {
	dir := t.TempDir()

	generated, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}

	if err := identity.SaveNew(dir, generated); err != nil {
		t.Fatalf("identity.SaveNew() error = %v", err)
	}

	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("identity.Load() error = %v", err)
	}

	challenge, err := auth.GenerateChallenge()
	if err != nil {
		t.Fatalf("auth.GenerateChallenge() error = %v", err)
	}

	signature, err := auth.Sign(
		loaded.PrivateKey(),
		challenge,
	)
	if err != nil {
		t.Fatalf("auth.Sign() error = %v", err)
	}

	if err := auth.Verify(
		loaded.PublicKey(),
		challenge,
		signature,
	); err != nil {
		t.Fatalf("auth.Verify() error = %v", err)
	}
}
