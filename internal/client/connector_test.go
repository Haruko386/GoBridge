package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/transport"
)

const connectorTestTimeout = 2 * time.Second

func TestConnectorEstablishesAuthenticatedSession(t *testing.T) {
	serverIdentity := connectorTestIdentity(t)
	clientIdentity := connectorTestIdentity(t)
	serverStore := connectorTestStore(t)
	clientStore := connectorTestStore(t)
	addConnectorPeer(t, serverStore, "client", clientIdentity)
	addConnectorPeer(t, clientStore, "server", serverIdentity)

	listener := connectorTestListener(t)
	defer listener.Close()
	serverTLSConfig, err := transport.NewServerTLSConfig(serverIdentity)
	if err != nil {
		t.Fatalf("transport.NewServerTLSConfig() error = %v", err)
	}
	serverResult := make(chan connectorServerResult, 1)
	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			serverResult <- connectorServerResult{err: err}
			return
		}
		session, err := transport.AcceptServerSession(
			context.Background(),
			rawConn,
			serverTLSConfig,
			serverStore.PublicKey,
			connectorTestTimeout,
		)
		serverResult <- connectorServerResult{session: session, err: err}
	}()

	cfg := connectorTestConfig(t, listener.Addr().String(), serverIdentity.NodeID())
	connector, err := NewConnector(cfg, clientIdentity, clientStore, connectorTestTimeout)
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}
	if connector.Address() != listener.Addr().String() {
		t.Fatalf("Address() = %q, want %q", connector.Address(), listener.Addr())
	}
	if connector.ServerNodeID() != serverIdentity.NodeID() {
		t.Fatalf("ServerNodeID() = %q, want %q", connector.ServerNodeID(), serverIdentity.NodeID())
	}

	clientSession, clientErr := connector.Connect(context.Background())
	serverSide := <-serverResult
	if clientErr != nil {
		t.Fatalf("Connect() error = %v", clientErr)
	}
	if serverSide.err != nil {
		t.Fatalf("AcceptServerSession() error = %v", serverSide.err)
	}
	defer clientSession.Close()
	defer serverSide.session.Close()

	if clientSession.PeerNodeID() != serverIdentity.NodeID() {
		t.Fatalf("client peer = %q, want %q", clientSession.PeerNodeID(), serverIdentity.NodeID())
	}
	if serverSide.session.PeerNodeID() != clientIdentity.NodeID() {
		t.Fatalf("server peer = %q, want %q", serverSide.session.PeerNodeID(), clientIdentity.NodeID())
	}
}

func TestNewConnectorRejectsInvalidState(t *testing.T) {
	serverIdentity := connectorTestIdentity(t)
	clientIdentity := connectorTestIdentity(t)
	pairedConfig := connectorTestConfig(t, "127.0.0.1:18790", serverIdentity.NodeID())

	t.Run("not paired", func(t *testing.T) {
		cfg, err := config.New(config.RoleClient)
		if err != nil {
			t.Fatalf("config.New() error = %v", err)
		}
		_, err = NewConnector(cfg, clientIdentity, connectorTestStore(t), time.Second)
		if !errors.Is(err, ErrNotPaired) {
			t.Fatalf("NewConnector() error = %v, want ErrNotPaired", err)
		}
	})

	t.Run("missing server peer", func(t *testing.T) {
		_, err := NewConnector(pairedConfig, clientIdentity, connectorTestStore(t), time.Second)
		if !errors.Is(err, peer.ErrNotFound) {
			t.Fatalf("NewConnector() error = %v, want peer.ErrNotFound", err)
		}
	})

	t.Run("disabled server peer", func(t *testing.T) {
		store := connectorTestStore(t)
		addConnectorPeer(t, store, "server", serverIdentity)
		if err := store.Disable(serverIdentity.NodeID()); err != nil {
			t.Fatalf("Disable() error = %v", err)
		}
		_, err := NewConnector(pairedConfig, clientIdentity, store, time.Second)
		if !errors.Is(err, ErrServerPeerDisabled) {
			t.Fatalf("NewConnector() error = %v, want ErrServerPeerDisabled", err)
		}
	})

	t.Run("nil peer store", func(t *testing.T) {
		_, err := NewConnector(pairedConfig, clientIdentity, nil, time.Second)
		if err == nil {
			t.Fatal("NewConnector() error = nil")
		}
	})

	t.Run("invalid timeout", func(t *testing.T) {
		store := connectorTestStore(t)
		addConnectorPeer(t, store, "server", serverIdentity)
		_, err := NewConnector(pairedConfig, clientIdentity, store, 0)
		if err == nil {
			t.Fatal("NewConnector() error = nil")
		}
	})
}

func TestConnectorObservesPeerStateChanges(t *testing.T) {
	serverIdentity := connectorTestIdentity(t)
	clientIdentity := connectorTestIdentity(t)
	store := connectorTestStore(t)
	addConnectorPeer(t, store, "server", serverIdentity)
	connector, err := NewConnector(
		connectorTestConfig(t, "127.0.0.1:18790", serverIdentity.NodeID()),
		clientIdentity,
		store,
		time.Second,
	)
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}

	if err := store.Disable(serverIdentity.NodeID()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if _, err := connector.Connect(context.Background()); !errors.Is(err, ErrServerPeerDisabled) {
		t.Fatalf("Connect() after Disable error = %v, want ErrServerPeerDisabled", err)
	}

	if err := store.Remove(serverIdentity.NodeID()); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := connector.Connect(context.Background()); !errors.Is(err, peer.ErrNotFound) {
		t.Fatalf("Connect() after Remove error = %v, want peer.ErrNotFound", err)
	}
}

func TestConnectorRejectsNilAndCanceledContexts(t *testing.T) {
	serverIdentity := connectorTestIdentity(t)
	clientIdentity := connectorTestIdentity(t)
	store := connectorTestStore(t)
	addConnectorPeer(t, store, "server", serverIdentity)
	connector, err := NewConnector(
		connectorTestConfig(t, "127.0.0.1:1", serverIdentity.NodeID()),
		clientIdentity,
		store,
		time.Second,
	)
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}

	if _, err := connector.Connect(nil); err == nil {
		t.Fatal("Connect(nil) error = nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := connector.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect(canceled) error = %v, want context.Canceled", err)
	}
}

type connectorServerResult struct {
	session *transport.Session
	err     error
}

func connectorTestIdentity(t *testing.T) identity.Identity {
	t.Helper()
	value, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	return value
}

func connectorTestStore(t *testing.T) *peer.Store {
	t.Helper()
	store, err := peer.Open(t.TempDir())
	if err != nil {
		t.Fatalf("peer.Open() error = %v", err)
	}
	return store
}

func addConnectorPeer(t *testing.T, store *peer.Store, name string, nodeIdentity identity.Identity) {
	t.Helper()
	value, err := peer.New(name, nodeIdentity.PublicKey())
	if err != nil {
		t.Fatalf("peer.New() error = %v", err)
	}
	if err := store.Add(value); err != nil {
		t.Fatalf("Store.Add() error = %v", err)
	}
}

func connectorTestConfig(t *testing.T, address, nodeID string) config.Config {
	t.Helper()
	cfg, err := config.New(config.RoleClient)
	if err != nil {
		t.Fatalf("config.New() error = %v", err)
	}
	cfg.Client.ServerAddress = address
	cfg.Client.ServerNodeID = nodeID
	return cfg
}

func connectorTestListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	return listener
}
