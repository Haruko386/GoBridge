package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/protocol"
)

const testSessionTimeout = 2 * time.Second

type serverSessionResult struct {
	session *Session
	err     error
}

func TestAuthenticatedTCPSession(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	serverConfig := newTestServerTLSConfig(t, serverIdentity)
	clientConfig := newTestClientTLSConfig(t, serverIdentity.PublicKey())

	listener := listenLocalTCP(t)
	defer listener.Close()

	serverDone := acceptTestSession(
		t,
		listener,
		serverConfig,
		func(nodeID string) (ed25519.PublicKey, bool) {
			if nodeID != clientIdentity.NodeID() {
				return nil, false
			}
			return clientIdentity.PublicKey(), true
		},
	)

	clientSession, err := DialClientSession(
		context.Background(),
		listener.Addr().String(),
		clientConfig,
		clientIdentity,
		testSessionTimeout,
	)
	if err != nil {
		t.Fatalf("DialClientSession() error = %v", err)
	}
	defer clientSession.Close()

	serverResult := <-serverDone
	if serverResult.err != nil {
		t.Fatalf("AcceptServerSession() error = %v", serverResult.err)
	}
	serverSession := serverResult.session
	defer serverSession.Close()

	setSessionDeadline(t, clientSession)
	setSessionDeadline(t, serverSession)

	if got := clientSession.PeerNodeID(); got != serverIdentity.NodeID() {
		t.Errorf("client peer Node ID = %q, want %q", got, serverIdentity.NodeID())
	}
	if got := serverSession.PeerNodeID(); got != clientIdentity.NodeID() {
		t.Errorf("server peer Node ID = %q, want %q", got, clientIdentity.NodeID())
	}
	if clientSession.LocalAddr() == nil || clientSession.RemoteAddr() == nil {
		t.Fatal("client session addresses must not be nil")
	}
	if serverSession.LocalAddr() == nil || serverSession.RemoteAddr() == nil {
		t.Fatal("server session addresses must not be nil")
	}

	if err := clientSession.WriteFrame(protocol.TypePing, []byte("ping")); err != nil {
		t.Fatalf("client WriteFrame(PING) error = %v", err)
	}
	ping, err := serverSession.ReadFrame()
	if err != nil {
		t.Fatalf("server ReadFrame() error = %v", err)
	}
	if ping.Type != protocol.TypePing || string(ping.Payload) != "ping" {
		t.Errorf("server frame = {%s %q}, want {PING %q}", ping.Type, ping.Payload, "ping")
	}

	if err := serverSession.WriteFrame(protocol.TypePong, []byte("pong")); err != nil {
		t.Fatalf("server WriteFrame(PONG) error = %v", err)
	}
	pong, err := clientSession.ReadFrame()
	if err != nil {
		t.Fatalf("client ReadFrame() error = %v", err)
	}
	if pong.Type != protocol.TypePong || string(pong.Payload) != "pong" {
		t.Errorf("client frame = {%s %q}, want {PONG %q}", pong.Type, pong.Payload, "pong")
	}
}

func TestSessionRejectsUnknownClient(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	listener := listenLocalTCP(t)
	defer listener.Close()

	serverDone := acceptTestSession(
		t,
		listener,
		newTestServerTLSConfig(t, serverIdentity),
		func(string) (ed25519.PublicKey, bool) { return nil, false },
	)

	_, clientErr := DialClientSession(
		context.Background(),
		listener.Addr().String(),
		newTestClientTLSConfig(t, serverIdentity.PublicKey()),
		clientIdentity,
		testSessionTimeout,
	)
	serverResult := <-serverDone

	if !errors.Is(clientErr, ErrAuthenticationFailed) {
		t.Errorf("DialClientSession() error = %v, want ErrAuthenticationFailed", clientErr)
	}
	if !errors.Is(serverResult.err, ErrUnknownPeer) {
		t.Errorf("AcceptServerSession() error = %v, want ErrUnknownPeer", serverResult.err)
	}
}

func TestSessionRejectsWrongServerIdentity(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	wrongServerIdentity := generateSessionIdentity(t)
	listener := listenLocalTCP(t)
	defer listener.Close()

	serverDone := acceptTestSession(
		t,
		listener,
		newTestServerTLSConfig(t, serverIdentity),
		func(nodeID string) (ed25519.PublicKey, bool) {
			return clientIdentity.PublicKey(), nodeID == clientIdentity.NodeID()
		},
	)

	_, clientErr := DialClientSession(
		context.Background(),
		listener.Addr().String(),
		newTestClientTLSConfig(t, wrongServerIdentity.PublicKey()),
		clientIdentity,
		testSessionTimeout,
	)
	serverResult := <-serverDone

	if !errors.Is(clientErr, ErrServerIdentityMismatch) {
		t.Errorf("DialClientSession() error = %v, want ErrServerIdentityMismatch", clientErr)
	}
	if serverResult.err == nil {
		t.Error("AcceptServerSession() error = nil, want TLS handshake error")
	}
}

func TestDialClientSessionHonorsCanceledContext(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DialClientSession(
		ctx,
		"127.0.0.1:1",
		newTestClientTLSConfig(t, serverIdentity.PublicKey()),
		clientIdentity,
		testSessionTimeout,
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DialClientSession() error = %v, want context.Canceled", err)
	}
}

func TestSessionArgumentValidation(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	serverConfig := newTestServerTLSConfig(t, serverIdentity)
	clientConfig := newTestClientTLSConfig(t, serverIdentity.PublicKey())
	lookup := func(string) (ed25519.PublicKey, bool) { return nil, false }

	serverConn, peerConn := net.Pipe()
	defer serverConn.Close()
	defer peerConn.Close()

	serverTests := []struct {
		name    string
		ctx     context.Context
		conn    net.Conn
		config  *tls.Config
		lookup  PublicKeyLookup
		timeout time.Duration
	}{
		{name: "nil context", conn: serverConn, config: serverConfig, lookup: lookup, timeout: time.Second},
		{name: "nil connection", ctx: context.Background(), config: serverConfig, lookup: lookup, timeout: time.Second},
		{name: "nil TLS config", ctx: context.Background(), conn: serverConn, lookup: lookup, timeout: time.Second},
		{name: "nil lookup", ctx: context.Background(), conn: serverConn, config: serverConfig, timeout: time.Second},
		{name: "invalid timeout", ctx: context.Background(), conn: serverConn, config: serverConfig, lookup: lookup},
	}

	for _, tt := range serverTests {
		t.Run("server "+tt.name, func(t *testing.T) {
			if _, err := AcceptServerSession(tt.ctx, tt.conn, tt.config, tt.lookup, tt.timeout); err == nil {
				t.Fatal("AcceptServerSession() error = nil, want an error")
			}
		})
	}

	clientTests := []struct {
		name     string
		ctx      context.Context
		address  string
		config   *tls.Config
		identity identity.Identity
		timeout  time.Duration
	}{
		{name: "nil context", address: "127.0.0.1:1", config: clientConfig, identity: clientIdentity, timeout: time.Second},
		{name: "empty address", ctx: context.Background(), config: clientConfig, identity: clientIdentity, timeout: time.Second},
		{name: "nil TLS config", ctx: context.Background(), address: "127.0.0.1:1", identity: clientIdentity, timeout: time.Second},
		{name: "invalid timeout", ctx: context.Background(), address: "127.0.0.1:1", config: clientConfig, identity: clientIdentity},
		{name: "invalid identity", ctx: context.Background(), address: "127.0.0.1:1", config: clientConfig, timeout: time.Second},
	}

	for _, tt := range clientTests {
		t.Run("client "+tt.name, func(t *testing.T) {
			if _, err := DialClientSession(tt.ctx, tt.address, tt.config, tt.identity, tt.timeout); err == nil {
				t.Fatal("DialClientSession() error = nil, want an error")
			}
		})
	}
}

func acceptTestSession(
	t *testing.T,
	listener net.Listener,
	tlsConfig *tls.Config,
	lookup PublicKeyLookup,
) <-chan serverSessionResult {
	t.Helper()

	result := make(chan serverSessionResult, 1)
	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			result <- serverSessionResult{err: err}
			return
		}

		session, err := AcceptServerSession(
			context.Background(),
			rawConn,
			tlsConfig,
			lookup,
			testSessionTimeout,
		)
		result <- serverSessionResult{session: session, err: err}
	}()

	return result
}

func listenLocalTCP(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	return listener
}

func newTestServerTLSConfig(t *testing.T, nodeIdentity identity.Identity) *tls.Config {
	t.Helper()

	config, err := NewServerTLSConfig(nodeIdentity)
	if err != nil {
		t.Fatalf("NewServerTLSConfig() error = %v", err)
	}
	return config
}

func newTestClientTLSConfig(t *testing.T, serverKey ed25519.PublicKey) *tls.Config {
	t.Helper()

	config, err := NewClientTLSConfig(serverKey)
	if err != nil {
		t.Fatalf("NewClientTLSConfig() error = %v", err)
	}
	return config
}

func generateSessionIdentity(t *testing.T) identity.Identity {
	t.Helper()

	nodeIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	return nodeIdentity
}

func setSessionDeadline(t *testing.T, session *Session) {
	t.Helper()

	if err := session.conn.SetDeadline(time.Now().Add(testSessionTimeout)); err != nil {
		t.Fatalf("session SetDeadline() error = %v", err)
	}
}

func TestSessionErrorMessagesContainContext(t *testing.T) {
	serverIdentity := generateSessionIdentity(t)
	clientIdentity := generateSessionIdentity(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DialClientSession(
		ctx,
		"127.0.0.1:1",
		newTestClientTLSConfig(t, serverIdentity.PublicKey()),
		clientIdentity,
		testSessionTimeout,
	)
	if err == nil || !strings.Contains(err.Error(), "dial server") {
		t.Fatalf("DialClientSession() error = %v, want dial context", err)
	}
}
