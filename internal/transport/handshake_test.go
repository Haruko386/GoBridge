package transport

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/protocol"
)

const testHandshakeTimeout = 2 * time.Second

type serverAuthResult struct {
	nodeID string
	err    error
}

func TestAuthenticationHandshake(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	clientIdentity := generateIdentity(t)

	lookup := func(
		nodeID string,
	) (ed25519.PublicKey, bool) {
		if nodeID != clientIdentity.NodeID() {
			return nil, false
		}

		return clientIdentity.PublicKey(), true
	}

	serverDone := make(chan serverAuthResult, 1)

	go func() {
		nodeID, err := AuthenticateServer(
			serverConn,
			lookup,
			testHandshakeTimeout,
		)

		serverDone <- serverAuthResult{
			nodeID: nodeID,
			err:    err,
		}
	}()

	clientErr := AuthenticateClient(
		clientConn,
		clientIdentity,
		testHandshakeTimeout,
	)

	serverResult := <-serverDone

	if clientErr != nil {
		t.Fatalf(
			"AuthenticateClient() error = %v",
			clientErr,
		)
	}

	if serverResult.err != nil {
		t.Fatalf(
			"AuthenticateServer() error = %v",
			serverResult.err,
		)
	}

	if serverResult.nodeID != clientIdentity.NodeID() {
		t.Errorf(
			"authenticated node ID = %q, want %q",
			serverResult.nodeID,
			clientIdentity.NodeID(),
		)
	}
}

func TestAuthenticationRejectsUnknownPeer(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	clientIdentity := generateIdentity(t)

	lookup := func(
		string,
	) (ed25519.PublicKey, bool) {
		return nil, false
	}

	serverDone := make(chan serverAuthResult, 1)

	go func() {
		nodeID, err := AuthenticateServer(
			serverConn,
			lookup,
			testHandshakeTimeout,
		)

		serverDone <- serverAuthResult{
			nodeID: nodeID,
			err:    err,
		}
	}()

	clientErr := AuthenticateClient(
		clientConn,
		clientIdentity,
		testHandshakeTimeout,
	)

	serverResult := <-serverDone

	if !errors.Is(
		clientErr,
		ErrAuthenticationFailed,
	) {
		t.Errorf(
			"AuthenticateClient() error = %v, want ErrAuthenticationFailed",
			clientErr,
		)
	}

	if !errors.Is(
		serverResult.err,
		ErrUnknownPeer,
	) {
		t.Errorf(
			"AuthenticateServer() error = %v, want ErrUnknownPeer",
			serverResult.err,
		)
	}
}

func TestAuthenticationRejectsMismatchedPublicKey(
	t *testing.T,
) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	clientIdentity := generateIdentity(t)
	otherIdentity := generateIdentity(t)

	lookup := func(
		string,
	) (ed25519.PublicKey, bool) {
		return otherIdentity.PublicKey(), true
	}

	serverDone := make(chan serverAuthResult, 1)

	go func() {
		nodeID, err := AuthenticateServer(
			serverConn,
			lookup,
			testHandshakeTimeout,
		)

		serverDone <- serverAuthResult{
			nodeID: nodeID,
			err:    err,
		}
	}()

	clientErr := AuthenticateClient(
		clientConn,
		clientIdentity,
		testHandshakeTimeout,
	)

	serverResult := <-serverDone

	if !errors.Is(
		clientErr,
		ErrAuthenticationFailed,
	) {
		t.Errorf(
			"AuthenticateClient() error = %v, want ErrAuthenticationFailed",
			clientErr,
		)
	}

	if !errors.Is(
		serverResult.err,
		ErrNodeIDMismatch,
	) {
		t.Errorf(
			"AuthenticateServer() error = %v, want ErrNodeIDMismatch",
			serverResult.err,
		)
	}
}

func TestClientRejectsUnexpectedMessage(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverDone := make(chan error, 1)

	go func() {
		serverDone <- protocol.NewEncoder(
			serverConn,
		).WriteFrame(
			protocol.TypePing,
			nil,
		)
	}()

	err := AuthenticateClient(
		clientConn,
		generateIdentity(t),
		testHandshakeTimeout,
	)

	if !errors.Is(err, ErrUnexpectedMessage) {
		t.Fatalf(
			"AuthenticateClient() error = %v, want ErrUnexpectedMessage",
			err,
		)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("server WriteFrame() error = %v", err)
	}
}

func TestClientHandshakeTimesOut(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	err := AuthenticateClient(
		clientConn,
		generateIdentity(t),
		50*time.Millisecond,
	)

	var networkError net.Error

	if !errors.As(err, &networkError) {
		t.Fatalf(
			"AuthenticateClient() error = %v, want net.Error",
			err,
		)
	}

	if !networkError.Timeout() {
		t.Fatalf(
			"AuthenticateClient() error = %v, want timeout",
			err,
		)
	}
}

func TestAuthResponseRoundTrip(t *testing.T) {
	nodeIdentity := generateIdentity(t)

	signature := make(
		[]byte,
		ed25519.SignatureSize,
	)

	payload, err := encodeAuthResponse(
		nodeIdentity.NodeID(),
		signature,
	)
	if err != nil {
		t.Fatalf(
			"encodeAuthResponse() error = %v",
			err,
		)
	}

	nodeIDBytes, decodedSignature, err :=
		decodeAuthResponse(payload)
	if err != nil {
		t.Fatalf(
			"decodeAuthResponse() error = %v",
			err,
		)
	}

	if len(nodeIDBytes) != 32 {
		t.Errorf(
			"decoded node ID length = %d, want 32",
			len(nodeIDBytes),
		)
	}

	if len(decodedSignature) != ed25519.SignatureSize {
		t.Errorf(
			"decoded signature length = %d, want %d",
			len(decodedSignature),
			ed25519.SignatureSize,
		)
	}
}

func TestDecodeAuthResponseRejectsInvalidLength(
	t *testing.T,
) {
	_, _, err := decodeAuthResponse(
		make([]byte, authResponseSize-1),
	)

	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf(
			"decodeAuthResponse() error = %v, want ErrMalformedResponse",
			err,
		)
	}
}

func generateIdentity(t *testing.T) identity.Identity {
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

func TestServerRejectsTamperedSignature(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	nodeIdentity := generateIdentity(t)

	lookup := func(
		nodeID string,
	) (ed25519.PublicKey, bool) {
		if nodeID != nodeIdentity.NodeID() {
			return nil, false
		}

		return nodeIdentity.PublicKey(), true
	}

	serverDone := make(chan serverAuthResult, 1)

	go func() {
		nodeID, err := AuthenticateServer(
			serverConn,
			lookup,
			testHandshakeTimeout,
		)

		serverDone <- serverAuthResult{
			nodeID: nodeID,
			err:    err,
		}
	}()

	decoder := protocol.NewDecoder(clientConn)
	encoder := protocol.NewEncoder(clientConn)

	frame, err := decoder.ReadFrame()
	if err != nil {
		t.Fatalf(
			"read challenge frame: %v",
			err,
		)
	}

	if frame.Type != protocol.TypeAuthChallenge {
		t.Fatalf(
			"frame type = %s, want AUTH_CHALLENGE",
			frame.Type,
		)
	}

	nodeIDBytes, err := hex.DecodeString(
		nodeIdentity.NodeID(),
	)
	if err != nil {
		t.Fatalf("DecodeString(node ID): %v", err)
	}

	payload := make([]byte, authResponseSize)
	copy(payload[:sha256.Size], nodeIDBytes)

	// The remaining 64 bytes are all zero and therefore not a valid
	// signature for this challenge.
	if err := encoder.WriteFrame(
		protocol.TypeAuthResponse,
		payload,
	); err != nil {
		t.Fatalf(
			"write tampered response: %v",
			err,
		)
	}

	result, err := decoder.ReadFrame()
	if err != nil {
		t.Fatalf(
			"read authentication result: %v",
			err,
		)
	}

	if result.Type != protocol.TypeAuthFailed {
		t.Errorf(
			"result type = %s, want AUTH_FAILED",
			result.Type,
		)
	}

	serverResult := <-serverDone

	if !errors.Is(
		serverResult.err,
		ErrAuthenticationFailed,
	) {
		t.Errorf(
			"server error = %v, want ErrAuthenticationFailed",
			serverResult.err,
		)
	}
}
