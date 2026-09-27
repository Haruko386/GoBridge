package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestGenerate(t *testing.T) {
	identity, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if err := identity.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if len(identity.PrivateKey()) == 0 {
		t.Fatal("PrivateKey() is empty")
	}

	if len(identity.PublicKey()) == 0 {
		t.Fatal("PublicKey() is empty")
	}

	if len(identity.NodeID()) != 64 {
		t.Errorf(
			"NodeID() length = %d, want 64",
			len(identity.NodeID()),
		)
	}

	sum := sha256.Sum256(identity.PublicKey())
	wantNodeID := hex.EncodeToString(sum[:])
	if identity.NodeID() != wantNodeID {
		t.Errorf("NodeID() = %q, want %q", identity.NodeID(), wantNodeID)
	}
}

func TestGenerateProducesUniqueIdentities(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}

	second, err := Generate()
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}

	if first.NodeID() == second.NodeID() {
		t.Fatal("two generated identities have the same node ID")
	}
}

func TestPrivateKeyReturnsCopy(t *testing.T) {
	identity, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	originalPrivateKey := identity.PrivateKey()
	returnedPrivateKey := identity.PrivateKey()
	returnedPrivateKey[0] ^= 0xff

	if !bytes.Equal(identity.PrivateKey(), originalPrivateKey) {
		t.Fatal("modifying returned private key changed stored private key")
	}
}

func TestPublicKeyReturnsCopy(t *testing.T) {
	identity, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	originalNodeID := identity.NodeID()

	publicKey := identity.PublicKey()
	publicKey[0] ^= 0xff

	if identity.NodeID() != originalNodeID {
		t.Fatal("modifying returned public key changed identity")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	original, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if err := SaveNew(dir, original); err != nil {
		t.Fatalf("SaveNew() error = %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.NodeID() != original.NodeID() {
		t.Errorf(
			"loaded NodeID() = %q, want %q",
			loaded.NodeID(),
			original.NodeID(),
		)
	}

	if !bytes.Equal(
		loaded.PublicKey(),
		original.PublicKey(),
	) {
		t.Fatal("loaded public key differs from original")
	}
}

func TestSaveNewDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()

	original, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if err := SaveNew(dir, original); err != nil {
		t.Fatalf("first SaveNew() error = %v", err)
	}

	replacement, err := Generate()
	if err != nil {
		t.Fatalf("replacement Generate() error = %v", err)
	}

	err = SaveNew(dir, replacement)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf(
			"second SaveNew() error = %v, want ErrAlreadyExists",
			err,
		)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.NodeID() != original.NodeID() {
		t.Fatal("existing identity was overwritten")
	}
}

func TestSaveNewRollsBackPrivateKeyWhenPublicKeyExists(t *testing.T) {
	dir := t.TempDir()
	existingPublicKey := []byte("existing public key")
	if err := os.WriteFile(PublicKeyPath(dir), existingPublicKey, 0o644); err != nil {
		t.Fatalf("WriteFile(public key) error = %v", err)
	}

	nodeIdentity, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	err = SaveNew(dir, nodeIdentity)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("SaveNew() error = %v, want ErrAlreadyExists", err)
	}

	if _, err := os.Stat(PrivateKeyPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private key exists after rollback: %v", err)
	}

	gotPublicKey, err := os.ReadFile(PublicKeyPath(dir))
	if err != nil {
		t.Fatalf("ReadFile(public key) error = %v", err)
	}
	if !bytes.Equal(gotPublicKey, existingPublicKey) {
		t.Fatal("existing public key was modified")
	}
}

func TestSaveNewRejectsInvalidIdentity(t *testing.T) {
	dir := t.TempDir()

	if err := SaveNew(dir, Identity{}); err == nil {
		t.Fatal("SaveNew() error = nil, want validation error")
	}
	if _, err := os.Stat(PrivateKeyPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private key exists after validation failure: %v", err)
	}
	if _, err := os.Stat(PublicKeyPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("public key exists after validation failure: %v", err)
	}
}

func TestLoadRejectsMismatchedKeys(t *testing.T) {
	dir := t.TempDir()

	first, err := Generate()
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}

	if err := SaveNew(dir, first); err != nil {
		t.Fatalf("SaveNew(first) error = %v", err)
	}

	secondDir := t.TempDir()

	second, err := Generate()
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}

	if err := SaveNew(secondDir, second); err != nil {
		t.Fatalf("SaveNew(second) error = %v", err)
	}

	secondPublicKey, err := os.ReadFile(PublicKeyPath(secondDir))
	if err != nil {
		t.Fatalf("ReadFile(second public key) error = %v", err)
	}

	if err := os.WriteFile(
		PublicKeyPath(dir),
		secondPublicKey,
		0o644,
	); err != nil {
		t.Fatalf("replace public key: %v", err)
	}

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want mismatched-key error")
	}
}

func TestLoadRejectsInvalidPEM(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(
		PrivateKeyPath(dir),
		[]byte("not a PEM file"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want invalid-PEM error")
	}
}

func TestLoadRejectsInvalidPublicKeyPEM(t *testing.T) {
	dir := t.TempDir()
	nodeIdentity, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if err := SaveNew(dir, nodeIdentity); err != nil {
		t.Fatalf("SaveNew() error = %v", err)
	}

	if err := os.WriteFile(PublicKeyPath(dir), []byte("not a PEM file"), 0o644); err != nil {
		t.Fatalf("WriteFile(public key) error = %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want invalid public-key PEM error")
	}
}

func TestPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not apply Unix permission bits")
	}

	dir := t.TempDir()

	identity, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if err := SaveNew(dir, identity); err != nil {
		t.Fatalf("SaveNew() error = %v", err)
	}

	info, err := os.Stat(PrivateKeyPath(dir))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf(
			"private key permissions = %o, want 600",
			got,
		)
	}
}
