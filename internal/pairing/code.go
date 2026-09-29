package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// GBP1.<server-public-key>.<random-secret>

const (
	CodePrefix = "GBP1"
	secretSize = 32
)

var (
	ErrInvalidCode = errors.New("invalid pair code")
	ErrExpiredCode = errors.New("pair code expired")
)

type Code struct {
	raw             string
	serverPublicKey ed25519.PublicKey
	secret          [secretSize]byte
}

func (c Code) String() string {
	return c.raw
}

func (c Code) ServerPublicKey() ed25519.PublicKey {
	return bytes.Clone(c.serverPublicKey)
}

func Parse(value string) (Code, error) {
	value = strings.TrimSpace(value)

	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != CodePrefix {
		return Code{}, ErrInvalidCode
	}

	encoding := base64.RawURLEncoding.Strict()

	publicKey, err := encoding.DecodeString(parts[1])
	if err != nil {
		return Code{}, fmt.Errorf("%w: decode server public key", ErrInvalidCode)
	}

	if len(publicKey) != ed25519.PublicKeySize {
		return Code{}, fmt.Errorf("%w: invalid server public key size", ErrInvalidCode)
	}

	secretBytes, err := encoding.DecodeString(parts[2])
	if err != nil {
		return Code{}, fmt.Errorf("%w: decode secret", ErrInvalidCode)
	}

	if len(secretBytes) != secretSize {
		return Code{}, fmt.Errorf("%w: invalid secret size", ErrInvalidCode)
	}

	var secret [secretSize]byte
	copy(secret[:], secretBytes)

	canonical := CodePrefix + "." + encoding.EncodeToString(publicKey) + "." + encoding.EncodeToString(secret[:])

	if value != canonical {
		return Code{}, fmt.Errorf("%w: non-canonical encoding", ErrInvalidCode)
	}

	return Code{
		raw:             canonical,
		serverPublicKey: bytes.Clone(publicKey),
		secret:          secret,
	}, nil
}

type Manager struct {
	mu sync.Mutex

	serverPublicKey ed25519.PublicKey
	entries         map[[sha256.Size]byte]time.Time

	now    func() time.Time
	random io.Reader
}

func NewManager(serverPublicKey ed25519.PublicKey) (*Manager, error) {
	return newManager(serverPublicKey, time.Now, rand.Reader)
}

func newManager(serverPublicKey ed25519.PublicKey, now func() time.Time, random io.Reader) (*Manager, error) {
	if len(serverPublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: invalid server public key size", ErrInvalidCode)
	}

	if now == nil {
		return nil, fmt.Errorf("%w: clock is nil", ErrInvalidCode)
	}

	if random == nil {
		return nil, fmt.Errorf("%w: random is nil", ErrInvalidCode)
	}

	return &Manager{
		serverPublicKey: bytes.Clone(serverPublicKey),
		entries:         make(map[[sha256.Size]byte]time.Time),
		now:             now,
		random:          random,
	}, nil
}

func (m *Manager) Create(ttl time.Duration) (Code, error) {
	if ttl <= 0 {
		return Code{}, errors.New("invalid ttl")
	}

	var secret [secretSize]byte
	if _, err := io.ReadFull(m.random, secret[:]); err != nil {
		return Code{}, err
	}

	publicKey := m.serverPublicKey
	if len(publicKey) != ed25519.PublicKeySize {
		return Code{}, fmt.Errorf("%w: invalid server public key size", ErrInvalidCode)
	}

	encoding := base64.RawURLEncoding

	raw := CodePrefix + "." + encoding.EncodeToString(publicKey) + "." + encoding.EncodeToString(secret[:])
	code := Code{
		raw:             raw,
		serverPublicKey: bytes.Clone(publicKey),
		secret:          secret,
	}

	digest := sha256.Sum256(secret[:])
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.removeExpiredLocked(now)
	m.entries[digest] = now.Add(ttl)

	return code, nil
}

func (m *Manager) Consume(code Code) error {
	if code.raw == "" || len(code.serverPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: invalid code", ErrInvalidCode)
	}

	if !bytes.Equal(code.ServerPublicKey(), m.serverPublicKey) {
		return fmt.Errorf("%w: invalid server public key", ErrInvalidCode)
	}

	digest := sha256.Sum256(code.secret[:])
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	expiredAt, exists := m.entries[digest]
	if !exists {
		return ErrInvalidCode
	}

	delete(m.entries, digest)

	if !now.Before(expiredAt) {
		return fmt.Errorf("%w: invalid code", ErrExpiredCode)
	}

	return nil
}

func (m *Manager) removeExpiredLocked(now time.Time) {
	for digest, expiredAt := range m.entries {
		if !now.Before(expiredAt) {
			delete(m.entries, digest)
		}
	}
}
