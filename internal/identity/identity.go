// Package identity manages a node's persistent Ed25519 identity.
package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	PrivateKeyFileName = "identity.key"
	PublicKeyFileName  = "identity.pub"
)

var ErrAlreadyExists = errors.New("identity already exists")

type Identity struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

// Generate creates a new Ed25519 identity using a cryptographically secure source of randomness.
func Generate() (Identity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, fmt.Errorf("generate Ed25519 key pair: %w", err)
	}

	return Identity{
		privateKey: privateKey,
		publicKey:  publicKey,
	}, nil
}

func (i Identity) PublicKey() ed25519.PublicKey {
	return bytes.Clone(i.publicKey)
}

func (i Identity) PrivateKey() ed25519.PrivateKey {
	return bytes.Clone(i.privateKey)
}

func (i Identity) NodeID() string {
	sum := sha256.Sum256(i.publicKey)
	return hex.EncodeToString(sum[:])
}

// Validate do validate
func (i Identity) Validate() error {
	// validate key size
	if len(i.publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: %d", len(i.publicKey))
	}
	if len(i.privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key size: %d", len(i.privateKey))
	}

	// validate `public key` using `private key`
	derivedPublicKey, ok := i.privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("derive Ed25519 public key from private key")
	}
	if !bytes.Equal(i.publicKey, derivedPublicKey) {
		return errors.New("identity public key mismatch")
	}

	return nil
}

/*
PrivateKeyPath get private key path
PublicKeyPath get public key path
*/
func PrivateKeyPath(dir string) string {
	return filepath.Join(dir, PrivateKeyFileName)
}

func PublicKeyPath(dir string) string {
	return filepath.Join(dir, PublicKeyFileName)
}

// SaveNew stores an identity without overwriting existing key files.
func SaveNew(dir string, identity Identity) error {
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("validate identity: %w", err)
	}

	privateKey, err := x509.MarshalPKCS8PrivateKey(identity.privateKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	publicKey, err := x509.MarshalPKIXPublicKey(identity.publicKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}

	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKey,
	})

	publicKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKey,
	})

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create identity directory: %w", err)
	}

	privatePath := PrivateKeyPath(dir)
	if err := writeNewFile(privatePath, privateKeyPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	publicPath := PublicKeyPath(dir)
	if err := writeNewFile(publicPath, publicKeyPEM, 0o644); err != nil {
		rollbackErr := os.Remove(privatePath)

		saveErr := fmt.Errorf("save public key: %w", err)
		if rollbackErr != nil {
			return errors.Join(saveErr, fmt.Errorf("rollback private key %s: %w", privatePath, rollbackErr))
		}
		return saveErr
	}

	return nil
}

// Load private and public key from dir and return an Identity
func Load(dir string) (Identity, error) {
	privateKey, err := loadPrivateKeyFromFile(PrivateKeyPath(dir))
	if err != nil {
		return Identity{}, fmt.Errorf("load private key: %w", err)
	}

	publicKey, err := loadPublicKeyFromFile(PublicKeyPath(dir))
	if err != nil {
		return Identity{}, fmt.Errorf("load public key: %w", err)
	}

	identity := Identity{
		privateKey: privateKey,
		publicKey:  publicKey,
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, fmt.Errorf("validate identity: %w", err)
	}

	return identity, nil
}

/*
loadPrivateKeyFromFile load private key from file
loadPublicKeyFromFile load public key from file
*/
func loadPrivateKeyFromFile(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity private key: %w", err)
	}

	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("decode private key PEM")
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("unexpected private key PEM type %q", block.Type)
	}

	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("unexpected data after private key PEM")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}

	return privateKey, nil
}

func loadPublicKeyFromFile(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity public key: %w", err)
	}

	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("decode public key PEM")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("unexpected public key PEM type %q", block.Type)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("unexpected data after public key PEM")
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	publicKey, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("public key is not Ed25519")
	}

	return publicKey, nil
}

// writeNewFile write privateKeyPEM(`data`) to `path`
func writeNewFile(path string, data []byte, permissions os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, path)
		}

		return err
	}

	// remove if not complete
	removeIncompleteFile := true
	defer func() {
		if removeIncompleteFile {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()

	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	// cancel
	removeIncompleteFile = false
	return nil
}
