package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/pairing"
	"github.com/Haruko386/GoBridge/internal/peer"
)

const testPairingTimeout = 2 * time.Second

func TestTLSPairingRoundTrip(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	manager, err := pairing.NewManager(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("pairing.NewManager() error = %v", err)
	}
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("manager.Create() error = %v", err)
	}

	serverDir := t.TempDir()
	clientDir := t.TempDir()
	serverStore := openPairingStore(t, serverDir)
	clientStore := openPairingStore(t, clientDir)
	serverTLSConfig := newTestServerTLSConfig(t, serverIdentity)
	listener := listenLocalTCP(t)
	defer listener.Close()

	serverResult := make(chan transportPairingResult, 1)
	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			serverResult <- transportPairingResult{err: err}
			return
		}
		pairedPeer, err := AcceptServerPairing(
			context.Background(),
			rawConn,
			serverTLSConfig,
			manager,
			serverIdentity,
			"machine-room-server",
			serverStore,
			testPairingTimeout,
		)
		serverResult <- transportPairingResult{peer: pairedPeer, err: err}
	}()

	serverPeer, clientErr := DialClientPairing(
		context.Background(),
		listener.Addr().String(),
		code,
		clientIdentity,
		"lab-client",
		clientStore,
		testPairingTimeout,
	)
	serverSide := <-serverResult

	if clientErr != nil {
		t.Fatalf("DialClientPairing() error = %v", clientErr)
	}
	if serverSide.err != nil {
		t.Fatalf("AcceptServerPairing() error = %v", serverSide.err)
	}
	if serverPeer.NodeID != serverIdentity.NodeID() {
		t.Fatalf("client received server NodeID %q, want %q", serverPeer.NodeID, serverIdentity.NodeID())
	}
	if serverSide.peer.NodeID != clientIdentity.NodeID() {
		t.Fatalf("server received client NodeID %q, want %q", serverSide.peer.NodeID, clientIdentity.NodeID())
	}

	// Open new instances to prove both sides reached durable storage.
	if _, err := openPairingStore(t, serverDir).Get(clientIdentity.NodeID()); err != nil {
		t.Fatalf("reopened server store Get(client) error = %v", err)
	}
	if _, err := openPairingStore(t, clientDir).Get(serverIdentity.NodeID()); err != nil {
		t.Fatalf("reopened client store Get(server) error = %v", err)
	}
}

func TestTLSPairingRejectsWrongServerIdentityBeforeSendingCode(t *testing.T) {
	expectedIdentity := generateSessionIdentity(t)
	wrongTLSIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	manager, err := pairing.NewManager(expectedIdentity.PublicKey())
	if err != nil {
		t.Fatalf("pairing.NewManager() error = %v", err)
	}
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("manager.Create() error = %v", err)
	}

	listener := listenLocalTCP(t)
	defer listener.Close()
	serverTLSConfig := newTestServerTLSConfig(t, wrongTLSIdentity)
	serverStore := openPairingStore(t, t.TempDir())
	clientStore := openPairingStore(t, t.TempDir())
	serverResult := make(chan error, 1)
	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		_, err = AcceptServerPairing(
			context.Background(),
			rawConn,
			serverTLSConfig,
			manager,
			expectedIdentity,
			"server",
			serverStore,
			testPairingTimeout,
		)
		serverResult <- err
	}()

	_, clientErr := DialClientPairing(
		context.Background(),
		listener.Addr().String(),
		code,
		clientIdentity,
		"client",
		clientStore,
		testPairingTimeout,
	)
	serverErr := <-serverResult

	if !errors.Is(clientErr, ErrServerIdentityMismatch) {
		t.Fatalf("DialClientPairing() error = %v, want ErrServerIdentityMismatch", clientErr)
	}
	if serverErr == nil {
		t.Fatal("AcceptServerPairing() error = nil after rejected TLS handshake")
	}

	// TLS failed before the client disclosed the code, so it remains usable.
	if err := manager.Consume(code); err != nil {
		t.Fatalf("pair code was consumed before TLS identity verification: %v", err)
	}
}

func TestDialClientPairingHonorsCanceledContext(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	manager, err := pairing.NewManager(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("pairing.NewManager() error = %v", err)
	}
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("manager.Create() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = DialClientPairing(
		ctx,
		"127.0.0.1:1",
		code,
		generateSessionIdentity(t),
		"client",
		openPairingStore(t, t.TempDir()),
		testPairingTimeout,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DialClientPairing() error = %v, want context.Canceled", err)
	}
}

type transportPairingResult struct {
	peer peer.Peer
	err  error
}

func openPairingStore(t *testing.T, dir string) *peer.Store {
	t.Helper()
	store, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open() error = %v", err)
	}
	return store
}
