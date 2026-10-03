package server

import (
	"context"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func TestPeerWatcherClosesDisabledSession(t *testing.T) {
	dir := t.TempDir()
	writer, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open(writer) error = %v", err)
	}
	paired := serverTestPeer(t, "client")
	if err := writer.Add(paired); err != nil {
		t.Fatalf("Store.Add() error = %v", err)
	}
	reader, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open(reader) error = %v", err)
	}

	registry := NewRegistry()
	session := &registryTestSession{nodeID: paired.NodeID}
	if _, err := registry.Register(session); err != nil {
		t.Fatalf("Registry.Register() error = %v", err)
	}
	watcher, err := NewPeerWatcher(reader, registry, time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPeerWatcher() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- watcher.Serve(ctx) }()

	if err := writer.Disable(paired.NodeID); err != nil {
		t.Fatalf("Store.Disable() error = %v", err)
	}
	serverTestEventually(t, func() bool { return session.closeCount.Load() == 1 }, "disabled session close")
	if registry.Len() != 0 {
		t.Fatalf("Registry.Len() = %d, want 0", registry.Len())
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("peer watcher did not stop")
	}
}

func TestNewPeerWatcherValidatesConfiguration(t *testing.T) {
	store, err := peer.Open(t.TempDir())
	if err != nil {
		t.Fatalf("peer.Open() error = %v", err)
	}
	if _, err := NewPeerWatcher(nil, NewRegistry(), time.Second, nil); err == nil {
		t.Fatal("NewPeerWatcher(nil store) error = nil")
	}
	if _, err := NewPeerWatcher(store, nil, time.Second, nil); err == nil {
		t.Fatal("NewPeerWatcher(nil registry) error = nil")
	}
	if _, err := NewPeerWatcher(store, NewRegistry(), 0, nil); err == nil {
		t.Fatal("NewPeerWatcher(zero interval) error = nil")
	}
}

func serverTestPeer(t *testing.T, name string) peer.Peer {
	t.Helper()
	nodeIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	paired, err := peer.New(name, nodeIdentity.PublicKey())
	if err != nil {
		t.Fatalf("peer.New() error = %v", err)
	}
	return paired
}
