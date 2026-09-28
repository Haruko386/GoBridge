package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
)

func TestTLSHandshake(t *testing.T) {
	serverIdentity := generateTLSIdentity(t)

	serverConfig, err := NewServerTLSConfig(
		serverIdentity,
	)
	if err != nil {
		t.Fatalf(
			"NewServerTLSConfig() error = %v",
			err,
		)
	}

	pinnedKey := serverIdentity.PublicKey()

	clientConfig, err := NewClientTLSConfig(pinnedKey)
	if err != nil {
		t.Fatalf(
			"NewClientTLSConfig() error = %v",
			err,
		)
	}

	// The TLS config must retain its own copy of the pinned key.
	pinnedKey[0] ^= 0xff

	serverRaw, clientRaw := net.Pipe()
	defer serverRaw.Close()
	defer clientRaw.Close()

	deadline := time.Now().Add(2 * time.Second)

	if err := serverRaw.SetDeadline(deadline); err != nil {
		t.Fatalf("server SetDeadline() error = %v", err)
	}
	if err := clientRaw.SetDeadline(deadline); err != nil {
		t.Fatalf("client SetDeadline() error = %v", err)
	}

	serverTLS := tls.Server(serverRaw, serverConfig)
	clientTLS := tls.Client(clientRaw, clientConfig)

	serverDone := make(chan error, 1)

	go func() {
		serverDone <- serverTLS.Handshake()
	}()

	clientErr := clientTLS.Handshake()
	serverErr := <-serverDone

	if clientErr != nil {
		t.Fatalf(
			"client TLS handshake error = %v",
			clientErr,
		)
	}
	if serverErr != nil {
		t.Fatalf(
			"server TLS handshake error = %v",
			serverErr,
		)
	}

	clientState := clientTLS.ConnectionState()

	if clientState.Version != tls.VersionTLS13 {
		t.Errorf(
			"TLS version = %x, want TLS 1.3",
			clientState.Version,
		)
	}

	if clientState.NegotiatedProtocol != TLSProtocol {
		t.Errorf(
			"negotiated protocol = %q, want %q",
			clientState.NegotiatedProtocol,
			TLSProtocol,
		)
	}
}

func TestTLSRejectsWrongServerIdentity(t *testing.T) {
	serverIdentity := generateTLSIdentity(t)
	wrongIdentity := generateTLSIdentity(t)

	serverConfig, err := NewServerTLSConfig(
		serverIdentity,
	)
	if err != nil {
		t.Fatalf(
			"NewServerTLSConfig() error = %v",
			err,
		)
	}

	clientConfig, err := NewClientTLSConfig(
		wrongIdentity.PublicKey(),
	)
	if err != nil {
		t.Fatalf(
			"NewClientTLSConfig() error = %v",
			err,
		)
	}

	serverRaw, clientRaw := net.Pipe()
	defer serverRaw.Close()
	defer clientRaw.Close()

	deadline := time.Now().Add(2 * time.Second)
	_ = serverRaw.SetDeadline(deadline)
	_ = clientRaw.SetDeadline(deadline)

	serverTLS := tls.Server(serverRaw, serverConfig)
	clientTLS := tls.Client(clientRaw, clientConfig)

	serverDone := make(chan error, 1)

	go func() {
		serverDone <- serverTLS.Handshake()
	}()

	clientErr := clientTLS.Handshake()
	_ = <-serverDone

	if !errors.Is(
		clientErr,
		ErrServerIdentityMismatch,
	) {
		t.Fatalf(
			"client handshake error = %v, want ErrServerIdentityMismatch",
			clientErr,
		)
	}
}

func TestClientTLSConfigRejectsInvalidKey(t *testing.T) {
	_, err := NewClientTLSConfig(nil)

	if err == nil {
		t.Fatal(
			"NewClientTLSConfig() error = nil, want an error",
		)
	}
}

func TestTLSRejectsMissingALPN(t *testing.T) {
	serverIdentity := generateTLSIdentity(t)

	serverConfig, err := NewServerTLSConfig(serverIdentity)
	if err != nil {
		t.Fatalf("NewServerTLSConfig() error = %v", err)
	}

	if serverConfig.VerifyConnection == nil {
		t.Fatal("server VerifyConnection is nil")
	}

	err = serverConfig.VerifyConnection(tls.ConnectionState{})
	if !errors.Is(err, ErrTLSProtocolMismatch) {
		t.Fatalf(
			"server handshake error = %v, want ErrTLSProtocolMismatch",
			err,
		)
	}
}

func generateTLSIdentity(
	t *testing.T,
) identity.Identity {
	t.Helper()

	nodeIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf(
			"identity.Generate() error = %v",
			err,
		)
	}

	return nodeIdentity
}
