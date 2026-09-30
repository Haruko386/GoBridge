package server

import (
	"errors"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

func TestRegistryRegisterGetAndNodeIDs(t *testing.T) {
	registry := NewRegistry()
	first := &registryTestSession{nodeID: "node-b"}
	second := &registryTestSession{nodeID: "node-a"}

	if _, err := registry.Register(first); err != nil {
		t.Fatalf("Register(first) error = %v", err)
	}
	if _, err := registry.Register(second); err != nil {
		t.Fatalf("Register(second) error = %v", err)
	}

	if got := registry.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if got, found := registry.Get(first.nodeID); !found || got != first {
		t.Fatalf("Get(%q) = (%v, %v), want (%v, true)", first.nodeID, got, found, first)
	}
	if _, found := registry.Get("missing"); found {
		t.Fatal("Get(missing) found = true")
	}
	if got, want := registry.NodeIDs(), []string{"node-a", "node-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeIDs() = %v, want %v", got, want)
	}
}

func TestRegistryRejectsInvalidSessions(t *testing.T) {
	registry := NewRegistry()

	if _, err := registry.Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
	if _, err := registry.Register(&registryTestSession{}); err == nil {
		t.Fatal("Register(empty node ID) error = nil")
	}
	if got := registry.Len(); got != 0 {
		t.Fatalf("Len() = %d after rejected registrations, want 0", got)
	}
}

func TestRegistryReplacementAndRegistrationOwnership(t *testing.T) {
	registry := NewRegistry()
	oldSession := &registryTestSession{nodeID: "node-1"}
	oldRegistration, err := registry.Register(oldSession)
	if err != nil {
		t.Fatalf("Register(old) error = %v", err)
	}

	newSession := &registryTestSession{nodeID: "node-1"}
	newRegistration, err := registry.Register(newSession)
	if err != nil {
		t.Fatalf("Register(new) error = %v", err)
	}
	if got := oldSession.closeCount.Load(); got != 1 {
		t.Fatalf("old session close count = %d, want 1", got)
	}
	if oldRegistration.Remove() {
		t.Fatal("old Registration.Remove() = true; it must not remove its replacement")
	}
	if got, found := registry.Get("node-1"); !found || got != newSession {
		t.Fatalf("Get(node-1) after old Remove = (%v, %v), want new session", got, found)
	}

	if !newRegistration.Remove() {
		t.Fatal("new Registration.Remove() = false, want true")
	}
	if newRegistration.Remove() {
		t.Fatal("second new Registration.Remove() = true, want false")
	}
	if got := registry.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
}

func TestRegistryClosesOutsideLock(t *testing.T) {
	registry := NewRegistry()
	oldSession := &registryTestSession{nodeID: "node-1"}
	oldSession.closeFunc = func() {
		// These calls would deadlock if Register held the write lock while
		// closing the replaced session.
		if registry.Len() != 1 {
			t.Errorf("Len() during Close = %d, want 1", registry.Len())
		}
		if got, found := registry.Get("node-1"); !found || got == oldSession {
			t.Errorf("Get(node-1) during Close = (%v, %v), want replacement", got, found)
		}
	}
	if _, err := registry.Register(oldSession); err != nil {
		t.Fatalf("Register(old) error = %v", err)
	}
	if _, err := registry.Register(&registryTestSession{nodeID: "node-1"}); err != nil {
		t.Fatalf("Register(replacement) error = %v", err)
	}
}

func TestRegistryReplacementReturnsRegistrationWithCloseError(t *testing.T) {
	registry := NewRegistry()
	closeErr := errors.New("old close failed")
	if _, err := registry.Register(&registryTestSession{nodeID: "node-1", closeErr: closeErr}); err != nil {
		t.Fatalf("Register(old) error = %v", err)
	}

	replacement := &registryTestSession{nodeID: "node-1"}
	registration, err := registry.Register(replacement)
	if registration == nil {
		t.Fatal("Register(replacement) registration = nil")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Register(replacement) error = %v, want wrapped close error", err)
	}
	if got, found := registry.Get("node-1"); !found || got != replacement {
		t.Fatalf("Get(node-1) = (%v, %v), want registered replacement", got, found)
	}
}

func TestRegistryCloseAndCloseAll(t *testing.T) {
	registry := NewRegistry()
	firstErr := errors.New("first close failed")
	secondErr := errors.New("second close failed")
	first := &registryTestSession{nodeID: "node-1", closeErr: firstErr}
	second := &registryTestSession{nodeID: "node-2", closeErr: secondErr}
	third := &registryTestSession{nodeID: "node-3"}
	for _, session := range []*registryTestSession{first, second, third} {
		if _, err := registry.Register(session); err != nil {
			t.Fatalf("Register(%s) error = %v", session.nodeID, err)
		}
	}

	if err := registry.Close("missing"); err != nil {
		t.Fatalf("Close(missing) error = %v", err)
	}
	if err := registry.Close("node-3"); err != nil {
		t.Fatalf("Close(node-3) error = %v", err)
	}
	if got := third.closeCount.Load(); got != 1 {
		t.Fatalf("node-3 close count = %d, want 1", got)
	}

	err := registry.CloseAll()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("CloseAll() error = %v, want both close errors", err)
	}
	if got := registry.Len(); got != 0 {
		t.Fatalf("Len() after CloseAll = %d, want 0", got)
	}
	if first.closeCount.Load() != 1 || second.closeCount.Load() != 1 {
		t.Fatalf("CloseAll close counts = (%d, %d), want (1, 1)", first.closeCount.Load(), second.closeCount.Load())
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	registry := NewRegistry()
	const workers = 8
	const iterations = 100

	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				nodeID := string(rune('a' + worker%4))
				registration, _ := registry.Register(&registryTestSession{nodeID: nodeID})
				registry.Get(nodeID)
				registry.Len()
				registry.NodeIDs()
				registration.Remove()
			}
		}(worker)
	}
	group.Wait()

	if err := registry.CloseAll(); err != nil {
		t.Fatalf("CloseAll() error = %v", err)
	}
}

type registryTestSession struct {
	nodeID     string
	closeErr   error
	closeFunc  func()
	closeCount atomic.Int32
}

func (s *registryTestSession) PeerNodeID() string { return s.nodeID }

func (s *registryTestSession) WriteFrame(protocol.MessageType, []byte) error { return nil }

func (s *registryTestSession) ReadFrame() (protocol.Frame, error) { return protocol.Frame{}, nil }

func (s *registryTestSession) LocalAddr() net.Addr { return nil }

func (s *registryTestSession) RemoteAddr() net.Addr { return nil }

func (s *registryTestSession) Close() error {
	s.closeCount.Add(1)
	if s.closeFunc != nil {
		s.closeFunc()
	}
	return s.closeErr
}
