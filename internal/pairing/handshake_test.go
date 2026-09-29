package pairing

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/auth"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func TestPairingHandshakePersistsBothPeers(t *testing.T) {
	serverIdentity := testIdentity(t)
	clientIdentity := testIdentity(t)
	manager, err := NewManager(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	code, err := manager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	serverDir := t.TempDir()
	clientDir := t.TempDir()
	serverStore := testStore(t, serverDir)
	clientStore := testStore(t, clientDir)
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverResult := make(chan pairingResult, 1)
	go func() {
		pairedPeer, err := AcceptServer(
			serverConn,
			manager,
			serverIdentity,
			"machine-room-server",
			serverStore,
			2*time.Second,
		)
		serverResult <- pairingResult{peer: pairedPeer, err: err}
	}()

	serverPeer, clientErr := PairClient(
		clientConn,
		code,
		clientIdentity,
		"lab-client",
		clientStore,
		2*time.Second,
	)
	serverSide := <-serverResult

	if clientErr != nil {
		t.Fatalf("PairClient() error = %v", clientErr)
	}
	if serverSide.err != nil {
		t.Fatalf("AcceptServer() error = %v", serverSide.err)
	}
	if serverPeer.NodeID != serverIdentity.NodeID() || serverPeer.Name != "machine-room-server" {
		t.Fatalf("PairClient() peer = %#v", serverPeer)
	}
	if serverSide.peer.NodeID != clientIdentity.NodeID() || serverSide.peer.Name != "lab-client" {
		t.Fatalf("AcceptServer() peer = %#v", serverSide.peer)
	}

	// Verify persistence by opening fresh Store instances from disk.
	reopenedServer := testStore(t, serverDir)
	reopenedClient := testStore(t, clientDir)
	if _, err := reopenedServer.Get(clientIdentity.NodeID()); err != nil {
		t.Fatalf("server store Get(client) error = %v", err)
	}
	if _, err := reopenedClient.Get(serverIdentity.NodeID()); err != nil {
		t.Fatalf("client store Get(server) error = %v", err)
	}
	if err := manager.Consume(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("Consume() after successful pairing error = %v, want ErrInvalidCode", err)
	}
}

func TestPairingRejectsUnknownCode(t *testing.T) {
	serverIdentity := testIdentity(t)
	clientIdentity := testIdentity(t)
	manager, err := NewManager(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// The code has the correct pinned server key, but its secret was issued
	// by another manager and is therefore unknown to the server.
	otherManager, err := NewManager(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("second NewManager() error = %v", err)
	}
	unknownCode, err := otherManager.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	serverStore := testStore(t, t.TempDir())
	clientStore := testStore(t, t.TempDir())
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		_, err := AcceptServer(
			serverConn,
			manager,
			serverIdentity,
			"server",
			serverStore,
			2*time.Second,
		)
		serverErr <- err
	}()

	_, clientErr := PairClient(
		clientConn,
		unknownCode,
		clientIdentity,
		"client",
		clientStore,
		2*time.Second,
	)
	acceptErr := <-serverErr

	if !errors.Is(clientErr, ErrPairingFailed) {
		t.Fatalf("PairClient() error = %v, want ErrPairingFailed", clientErr)
	}
	if !errors.Is(acceptErr, ErrPairingFailed) || !errors.Is(acceptErr, ErrInvalidCode) {
		t.Fatalf("AcceptServer() error = %v, want ErrPairingFailed and ErrInvalidCode", acceptErr)
	}
	if got := serverStore.List(); len(got) != 0 {
		t.Fatalf("server peers = %#v, want empty", got)
	}
	if got := clientStore.List(); len(got) != 0 {
		t.Fatalf("client peers = %#v, want empty", got)
	}
}

func TestPairingSignatureBindsRequestFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	challenge, err := auth.GenerateChallenge()
	if err != nil {
		t.Fatalf("GenerateChallenge() error = %v", err)
	}
	message := pairingSigningMessage(challenge, "client", publicKey, "code-one")
	signature := ed25519.Sign(privateKey, message)
	if !ed25519.Verify(publicKey, message, signature) {
		t.Fatal("signature did not verify for original request")
	}

	otherKey := testPublicKey(t)
	changed := [][]byte{
		pairingSigningMessage(challenge, "other-name", publicKey, "code-one"),
		pairingSigningMessage(challenge, "client", otherKey, "code-one"),
		pairingSigningMessage(challenge, "client", publicKey, "code-two"),
	}
	for i, candidate := range changed {
		if bytes.Equal(candidate, message) {
			t.Fatalf("changed message %d equals original", i)
		}
		if ed25519.Verify(publicKey, candidate, signature) {
			t.Fatalf("signature verified changed message %d", i)
		}
	}
}

func TestAcceptServerRejectsMismatchedManagerIdentity(t *testing.T) {
	serverIdentity := testIdentity(t)
	manager, err := NewManager(testIdentity(t).PublicKey())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	_, err = AcceptServer(
		serverConn,
		manager,
		serverIdentity,
		"server",
		testStore(t, t.TempDir()),
		time.Second,
	)
	if !errors.Is(err, ErrPairingServerKeyMismatch) {
		t.Fatalf("AcceptServer() error = %v, want ErrPairingServerKeyMismatch", err)
	}
}

type pairingResult struct {
	peer peer.Peer
	err  error
}

func testIdentity(t *testing.T) identity.Identity {
	t.Helper()
	value, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	return value
}

func testStore(t *testing.T, dir string) *peer.Store {
	t.Helper()
	store, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open() error = %v", err)
	}
	return store
}
