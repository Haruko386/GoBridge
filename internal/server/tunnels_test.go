package server

import (
	"context"
	"errors"
	"testing"

	"github.com/Haruko386/GoBridge/internal/proxy"
)

func TestTunnelRegistryLifecycle(t *testing.T) {
	registry := NewTunnelRegistry()
	if _, _, err := registry.Single(); !errors.Is(err, ErrProxyProviderOffline) {
		t.Fatalf("Single() error = %v, want ErrProxyProviderOffline", err)
	}

	firstOpen := func(context.Context) (proxy.Endpoint, error) { return nil, nil }
	first, err := registry.Register("node-b", firstOpen)
	if err != nil {
		t.Fatalf("Register(first) error = %v", err)
	}
	if registry.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", registry.Len())
	}
	nodeID, _, err := registry.Single()
	if err != nil || nodeID != "node-b" {
		t.Fatalf("Single() = (%q, %v), want node-b", nodeID, err)
	}

	second, err := registry.Register("node-a", func(context.Context) (proxy.Endpoint, error) { return nil, nil })
	if err != nil {
		t.Fatalf("Register(second) error = %v", err)
	}
	if _, _, err := registry.Single(); !errors.Is(err, ErrMultipleProxyProviders) {
		t.Fatalf("Single() error = %v, want ErrMultipleProxyProviders", err)
	}
	if got := registry.NodeIDs(); len(got) != 2 || got[0] != "node-a" || got[1] != "node-b" {
		t.Fatalf("NodeIDs() = %v, want [node-a node-b]", got)
	}

	if !second.Remove() || second.Remove() {
		t.Fatal("second registration removal is not idempotent")
	}
	if !first.Remove() {
		t.Fatal("first registration was not removed")
	}
}

func TestTunnelRegistryReplacementKeepsNewRegistration(t *testing.T) {
	registry := NewTunnelRegistry()
	first, err := registry.Register("node", func(context.Context) (proxy.Endpoint, error) { return nil, nil })
	if err != nil {
		t.Fatalf("Register(first) error = %v", err)
	}
	second, err := registry.Register("node", func(context.Context) (proxy.Endpoint, error) { return nil, nil })
	if err != nil {
		t.Fatalf("Register(second) error = %v", err)
	}

	if first.Remove() {
		t.Fatal("old registration removed its replacement")
	}
	if registry.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", registry.Len())
	}
	if !second.Remove() {
		t.Fatal("replacement registration was not removed")
	}
}

func TestTunnelRegistryValidatesRegistration(t *testing.T) {
	registry := NewTunnelRegistry()
	open := func(context.Context) (proxy.Endpoint, error) { return nil, nil }

	if _, err := registry.Register("", open); err == nil {
		t.Fatal("Register(empty node ID) error = nil")
	}
	if _, err := registry.Register("node", nil); err == nil {
		t.Fatal("Register(nil open) error = nil")
	}
	var registration *TunnelRegistration
	if registration.Remove() {
		t.Fatal("nil registration Remove() = true")
	}
}
