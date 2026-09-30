package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/transport"
)

const serverTestTimeout = 3 * time.Second

func TestNewServerValidatesDependencies(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	validLookup := transport.PublicKeyLookup(func(string) (ed25519.PublicKey, bool) { return nil, false })
	validHandler := SessionHandler(func(context.Context, Session) error { return nil })

	tests := []struct {
		name        string
		listenerNil bool
		identity    identity.Identity
		lookup      transport.PublicKeyLookup
		registry    *Registry
		timeout     time.Duration
		handler     SessionHandler
	}{
		{name: "nil listener", listenerNil: true, identity: serverIdentity, lookup: validLookup, registry: NewRegistry(), timeout: time.Second, handler: validHandler},
		{name: "invalid identity", identity: identity.Identity{}, lookup: validLookup, registry: NewRegistry(), timeout: time.Second, handler: validHandler},
		{name: "nil lookup", identity: serverIdentity, registry: NewRegistry(), timeout: time.Second, handler: validHandler},
		{name: "nil registry", identity: serverIdentity, lookup: validLookup, timeout: time.Second, handler: validHandler},
		{name: "zero timeout", identity: serverIdentity, lookup: validLookup, registry: NewRegistry(), handler: validHandler},
		{name: "nil handler", identity: serverIdentity, lookup: validLookup, registry: NewRegistry(), timeout: time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var listener net.Listener
			if !test.listenerNil {
				listener = serverTestListener(t)
				defer listener.Close()
			}

			if _, err := NewServer(listener, test.identity, test.lookup, test.registry, test.timeout, test.handler, nil); err == nil {
				t.Fatal("NewServer() error = nil")
			}
		})
	}
}

func TestServerAcceptsAuthenticatedClientAndCleansUp(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	clientIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	registry := NewRegistry()
	handled := make(chan Session, 1)

	server := serverTestNew(t, listener, serverIdentity, serverTestLookup(clientIdentity), registry,
		func(ctx context.Context, session Session) error {
			handled <- session
			<-ctx.Done()
			return nil
		}, nil)
	cancel, result := serverTestStart(t, server)

	clientSession := serverTestDial(t, listener.Addr().String(), serverIdentity, clientIdentity)
	defer clientSession.Close()

	serverSession := serverTestReceive(t, handled, "authenticated session")
	if got, want := serverSession.PeerNodeID(), clientIdentity.NodeID(); got != want {
		t.Fatalf("server peer node ID = %q, want %q", got, want)
	}
	if got, found := registry.Get(clientIdentity.NodeID()); !found || got != serverSession {
		t.Fatalf("Registry.Get(client) = (%v, %v), want handled session", got, found)
	}

	serverTestStop(t, cancel, result)
	if got := registry.Len(); got != 0 {
		t.Fatalf("Registry.Len() after shutdown = %d, want 0", got)
	}
}

func TestServerAuthenticationFailureDoesNotStopAcceptLoop(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	clientIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	errorsReported := make(chan error, 4)
	handled := make(chan struct{}, 1)

	server := serverTestNew(t, listener, serverIdentity, serverTestLookup(clientIdentity), NewRegistry(),
		func(context.Context, Session) error {
			handled <- struct{}{}
			return nil
		}, func(err error) {
			errorsReported <- err
		})
	cancel, result := serverTestStart(t, server)
	defer serverTestStop(t, cancel, result)

	invalid, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial(invalid client) error = %v", err)
	}
	if _, err := invalid.Write([]byte("not a TLS connection")); err != nil {
		t.Fatalf("invalid client Write() error = %v", err)
	}
	_ = invalid.Close()

	authErr := serverTestReceive(t, errorsReported, "authentication error")
	if !strings.Contains(authErr.Error(), "accept server session") {
		t.Fatalf("reported error = %v, want accept server session context", authErr)
	}

	valid := serverTestDial(t, listener.Addr().String(), serverIdentity, clientIdentity)
	defer valid.Close()
	serverTestReceive(t, handled, "valid client after invalid client")
}

func TestServerSlowClientDoesNotBlockOtherClients(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	clientIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	handled := make(chan struct{}, 1)

	server := serverTestNew(t, listener, serverIdentity, serverTestLookup(clientIdentity), NewRegistry(),
		func(context.Context, Session) error {
			handled <- struct{}{}
			return nil
		}, nil)
	cancel, result := serverTestStart(t, server)
	defer serverTestStop(t, cancel, result)

	slowClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial(slow client) error = %v", err)
	}
	defer slowClient.Close()

	valid := serverTestDial(t, listener.Addr().String(), serverIdentity, clientIdentity)
	defer valid.Close()
	serverTestReceive(t, handled, "valid client while slow handshake is pending")
}

func TestServerRejectsUnknownPeerAndContinues(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	knownIdentity := serverTestIdentity(t)
	unknownIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	registry := NewRegistry()
	errorsReported := make(chan error, 4)
	handled := make(chan struct{}, 1)

	server := serverTestNew(t, listener, serverIdentity, serverTestLookup(knownIdentity), registry,
		func(context.Context, Session) error {
			handled <- struct{}{}
			return nil
		}, func(err error) {
			errorsReported <- err
		})
	cancel, result := serverTestStart(t, server)
	defer serverTestStop(t, cancel, result)

	clientTLS, err := transport.NewClientTLSConfig(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("transport.NewClientTLSConfig() error = %v", err)
	}
	if _, err := transport.DialClientSession(context.Background(), listener.Addr().String(), clientTLS, unknownIdentity, serverTestTimeout); !errors.Is(err, transport.ErrAuthenticationFailed) {
		t.Fatalf("unknown DialClientSession() error = %v, want ErrAuthenticationFailed", err)
	}
	serverTestReceive(t, errorsReported, "unknown-peer error")
	if got := registry.Len(); got != 0 {
		t.Fatalf("Registry.Len() after rejected peer = %d, want 0", got)
	}

	known := serverTestDial(t, listener.Addr().String(), serverIdentity, knownIdentity)
	defer known.Close()
	serverTestReceive(t, handled, "known client after rejected peer")
}

func TestServerReportsHandlerErrorsAndRemovesSession(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	clientIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	registry := NewRegistry()
	handlerErr := errors.New("handler failed")
	errorsReported := make(chan error, 1)

	server := serverTestNew(t, listener, serverIdentity, serverTestLookup(clientIdentity), registry,
		func(context.Context, Session) error { return handlerErr },
		func(err error) { errorsReported <- err })
	cancel, result := serverTestStart(t, server)
	defer serverTestStop(t, cancel, result)

	client := serverTestDial(t, listener.Addr().String(), serverIdentity, clientIdentity)
	defer client.Close()

	reported := serverTestReceive(t, errorsReported, "handler error")
	if !errors.Is(reported, handlerErr) {
		t.Fatalf("reported error = %v, want wrapped handler error", reported)
	}
	serverTestEventually(t, func() bool { return registry.Len() == 0 }, "session removal after handler exits")
}

func TestServerServeRejectsNilContext(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	defer listener.Close()
	server := serverTestNew(t, listener, serverIdentity, func(string) (ed25519.PublicKey, bool) { return nil, false }, NewRegistry(),
		func(context.Context, Session) error { return nil }, nil)

	if err := server.Serve(nil); err == nil {
		t.Fatal("Serve(nil) error = nil")
	}
}

func serverTestNew(
	t *testing.T,
	listener net.Listener,
	serverIdentity identity.Identity,
	lookup transport.PublicKeyLookup,
	registry *Registry,
	handler SessionHandler,
	report ErrorHandler,
) *Server {
	t.Helper()
	server, err := NewServer(listener, serverIdentity, lookup, registry, serverTestTimeout, handler, report)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func serverTestStart(t *testing.T, server *Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(ctx)
	}()
	return cancel, result
}

func serverTestStop(t *testing.T, cancel context.CancelFunc, result <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(serverTestTimeout):
		t.Fatal("Serve() did not stop after context cancellation")
	}
}

func serverTestDial(t *testing.T, address string, serverIdentity, clientIdentity identity.Identity) *transport.Session {
	t.Helper()
	tlsConfig, err := transport.NewClientTLSConfig(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("transport.NewClientTLSConfig() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), serverTestTimeout)
	defer cancel()
	session, err := transport.DialClientSession(ctx, address, tlsConfig, clientIdentity, serverTestTimeout)
	if err != nil {
		t.Fatalf("transport.DialClientSession() error = %v", err)
	}
	return session
}

func serverTestLookup(nodeIdentity identity.Identity) transport.PublicKeyLookup {
	return func(nodeID string) (ed25519.PublicKey, bool) {
		if nodeID != nodeIdentity.NodeID() {
			return nil, false
		}
		return nodeIdentity.PublicKey(), true
	}
}

func serverTestIdentity(t *testing.T) identity.Identity {
	t.Helper()
	value, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	return value
}

func serverTestListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	return listener
}

func serverTestReceive[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(serverTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func serverTestEventually(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(serverTestTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
