package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeCreateParseAndConsume(t *testing.T) {
	publicKey := testPublicKey(t)
	manager, err := NewManager(publicKey)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	created, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	parsed, err := Parse(created.String())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.String() != created.String() {
		t.Fatalf("parsed String() = %q, want %q", parsed.String(), created.String())
	}
	if !bytes.Equal(parsed.ServerPublicKey(), publicKey) {
		t.Fatal("parsed server public key does not match")
	}
	if err := manager.Consume(parsed); err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if err := manager.Consume(parsed); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second Consume() error = %v, want ErrInvalidCode", err)
	}
}

func TestCodePublicKeyIsCloned(t *testing.T) {
	publicKey := testPublicKey(t)
	want := bytes.Clone(publicKey)
	manager, err := NewManager(publicKey)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	publicKey[0] ^= 0xff
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !bytes.Equal(code.ServerPublicKey(), want) {
		t.Fatal("mutating NewManager input changed its server public key")
	}

	returned := code.ServerPublicKey()
	returned[0] ^= 0xff
	if !bytes.Equal(code.ServerPublicKey(), want) {
		t.Fatal("mutating ServerPublicKey() result changed the Code")
	}
}

func TestParseRejectsInvalidCodes(t *testing.T) {
	publicKey := testPublicKey(t)
	manager, err := NewManager(publicKey)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	valid, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"wrong prefix", "GBP2.a.b"},
		{"missing part", "GBP1.a"},
		{"extra part", "GBP1.a.b.c"},
		{"invalid public key base64", "GBP1.!.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"short public key", "GBP1.YQ.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"invalid secret base64", "GBP1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA.!"},
		{"short secret", "GBP1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA.YQ"},
		{"padded encoding", valid.String() + "="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.value); !errors.Is(err, ErrInvalidCode) {
				t.Fatalf("Parse() error = %v, want ErrInvalidCode", err)
			}
		})
	}
}

func TestCodeExpiresAtBoundary(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	manager, err := newManager(testPublicKey(t), func() time.Time { return now }, rand.Reader)
	if err != nil {
		t.Fatalf("newManager() error = %v", err)
	}
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	now = now.Add(time.Minute)
	if err := manager.Consume(code); !errors.Is(err, ErrExpiredCode) {
		t.Fatalf("Consume() error = %v, want ErrExpiredCode", err)
	}
	if err := manager.Consume(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second Consume() error = %v, want ErrInvalidCode", err)
	}
}

func TestManagerRejectsCodeFromAnotherServer(t *testing.T) {
	first, err := NewManager(testPublicKey(t))
	if err != nil {
		t.Fatalf("first NewManager() error = %v", err)
	}
	second, err := NewManager(testPublicKey(t))
	if err != nil {
		t.Fatalf("second NewManager() error = %v", err)
	}
	code, err := first.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := second.Consume(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("Consume() error = %v, want ErrInvalidCode", err)
	}
}

func TestConcurrentConsumeSucceedsOnce(t *testing.T) {
	manager, err := NewManager(testPublicKey(t))
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if manager.Consume(code) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful Consume() calls = %d, want 1", got)
	}
}

func TestManagerInputErrors(t *testing.T) {
	publicKey := testPublicKey(t)
	if _, err := NewManager(nil); err == nil {
		t.Fatal("NewManager(nil) error = nil")
	}
	if _, err := newManager(publicKey, nil, rand.Reader); err == nil {
		t.Fatal("newManager(nil clock) error = nil")
	}
	if _, err := newManager(publicKey, time.Now, nil); err == nil {
		t.Fatal("newManager(nil random) error = nil")
	}

	manager, err := NewManager(publicKey)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if _, err := manager.Create(0); err == nil {
		t.Fatal("Create(0) error = nil")
	}

	manager, err = newManager(publicKey, time.Now, errorReader{})
	if err != nil {
		t.Fatalf("newManager() error = %v", err)
	}
	if _, err := manager.Create(time.Minute); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Create() error = %v, want io.ErrUnexpectedEOF", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func testPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return publicKey
}
