package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Haruko386/GoBridge/internal/auth"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/protocol"
)

const (
	DefaultHandshakeTimeout = 10 * time.Second
	authResponseSize        = sha256.Size + ed25519.SignatureSize
)

var (
	ErrAuthenticationFailed = errors.New("authentication failed")
	ErrUnknownPeer          = errors.New("unknown peer")
	ErrNodeIDMismatch       = errors.New("node ID does not match public key")
	ErrUnexpectedMessage    = errors.New("unexpected handshake message")
	ErrMalformedResponse    = errors.New("malformed authentication response")
)

// PublicKeyLookup finds the previously paired public key for a Node ID.
//
// Returning false means the Node ID is not a paired or enabled peer.
type PublicKeyLookup func(nodeID string) (ed25519.PublicKey, bool)

func AuthenticateServer(conn net.Conn, lookup PublicKeyLookup, timeout time.Duration) (string, error) {
	if conn == nil {
		return "", errors.New("server authentication connection is nil")
	}
	if lookup == nil {
		return "", errors.New("public key lookup is nil")
	}
	if timeout <= 0 {
		return "", errors.New("handshake timeout must be positive")
	}

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", fmt.Errorf("set server handshake deadline: %w", err)
	}

	defer func() {
		_ = conn.SetDeadline(time.Time{})
	}()

	encoder := protocol.NewEncoder(conn)
	decoder := protocol.NewDecoder(conn)

	challenge, err := auth.GenerateChallenge()
	if err != nil {
		return "", fmt.Errorf("generate server challenge: %w", err)
	}

	if err := encoder.WriteFrame(protocol.TypeAuthChallenge, challenge.Bytes()); err != nil {
		return "", fmt.Errorf("send authentication challenge: %w", err)
	}

	frame, err := decoder.ReadFrame()
	if err != nil {
		return "", fmt.Errorf("read authentication response: %w", err)
	}

	if frame.Type != protocol.TypeAuthResponse {
		cause := fmt.Errorf("%w: got %s, want %s", ErrUnexpectedMessage, frame.Type, protocol.TypeAuthResponse)

		return "", rejectAuthentication(encoder, cause)
	}

	nodeIDBytes, signature, err := decodeAuthResponse(frame.Payload)
	if err != nil {
		return "", rejectAuthentication(encoder, err)
	}

	nodeID := hex.EncodeToString(nodeIDBytes[:])

	publicKey, found := lookup(nodeID)
	if !found {
		cause := errors.Join(
			ErrAuthenticationFailed,
			ErrUnknownPeer,
		)
		return "", rejectAuthentication(encoder, cause)
	}

	if len(publicKey) != ed25519.PublicKeySize {
		cause := fmt.Errorf(
			"%w: stored public key length is %d",
			ErrAuthenticationFailed,
			len(publicKey),
		)
		return "", rejectAuthentication(encoder, cause)
	}

	expectedNodeID := sha256.Sum256(publicKey)
	if !bytes.Equal(expectedNodeID[:], nodeIDBytes[:]) {
		cause := errors.Join(
			ErrAuthenticationFailed,
			ErrNodeIDMismatch,
		)
		return "", rejectAuthentication(encoder, cause)
	}

	if err := auth.Verify(publicKey, challenge, signature); err != nil {
		cause := fmt.Errorf(
			"%w: verify signature: %v",
			ErrAuthenticationFailed,
			err,
		)
		return "", rejectAuthentication(encoder, cause)
	}

	if err := encoder.WriteFrame(protocol.TypeAuthOK, nil); err != nil {
		return "", fmt.Errorf("send authentication success: %w", err)
	}

	return nodeID, nil
}

func AuthenticateClient(conn net.Conn, nodeIdentity identity.Identity, timeout time.Duration) error {
	if conn == nil {
		return errors.New("client authentication connection is nil")
	}
	if timeout <= 0 {
		return errors.New("handshake timeout must be positive")
	}

	if err := nodeIdentity.Validate(); err != nil {
		return fmt.Errorf("validate client identity: %w", err)
	}

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set client handshake deadline: %w", err)
	}

	defer func() {
		_ = conn.SetDeadline(time.Time{})
	}()

	encoder := protocol.NewEncoder(conn)
	decoder := protocol.NewDecoder(conn)

	frame, err := decoder.ReadFrame()
	if err != nil {
		return fmt.Errorf("read authentication challenge: %w", err)
	}

	if frame.Type != protocol.TypeAuthChallenge {
		return fmt.Errorf("%w: got %s, want %s", ErrUnexpectedMessage, frame.Type, protocol.TypeAuthChallenge)
	}

	challenge, err := auth.ParseChallenge(frame.Payload)
	if err != nil {
		return fmt.Errorf("parse authentication challenge: %w", err)
	}

	signature, err := auth.Sign(nodeIdentity.PrivateKey(), challenge)
	if err != nil {
		return fmt.Errorf("sign authentication challenge: %w", err)
	}

	response, err := encodeAuthResponse(nodeIdentity.NodeID(), signature)
	if err != nil {
		return fmt.Errorf("encode authentication response: %w", err)
	}

	if err := encoder.WriteFrame(protocol.TypeAuthResponse, response); err != nil {
		return fmt.Errorf("send authentication response: %w", err)
	}

	result, err := decoder.ReadFrame()
	if err != nil {
		return fmt.Errorf("read authentication result: %w", err)
	}

	switch result.Type {
	case protocol.TypeAuthOK:
		if len(result.Payload) != 0 {
			return fmt.Errorf("%w: AUTH_OK payload must be empty", ErrUnexpectedMessage)
		}
		return nil
	case protocol.TypeAuthFailed:
		return ErrAuthenticationFailed
	default:
		return fmt.Errorf("%w: got %s, want AUTH_OK or AUTH_FAILED", ErrUnexpectedMessage, result.Type)
	}
}

func encodeAuthResponse(nodeID string, signature []byte) ([]byte, error) {
	nodeIDBytes, err := hex.DecodeString(nodeID)
	if err != nil {
		return nil, fmt.Errorf("decode node ID: %w", err)
	}

	if len(nodeIDBytes) != sha256.Size {
		return nil, fmt.Errorf("invalid node ID length %d: want %d", len(nodeIDBytes), sha256.Size)
	}

	if len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid signature length %d: want %d", len(signature), ed25519.SignatureSize)
	}

	payload := make([]byte, authResponseSize)

	copy(payload[:sha256.Size], nodeIDBytes)
	copy(payload[sha256.Size:], signature)

	return payload, nil
}

func decodeAuthResponse(payload []byte) ([sha256.Size]byte, []byte, error) {
	if len(payload) != authResponseSize {
		return [sha256.Size]byte{}, nil, fmt.Errorf("%w: got %d bytes, want %d", ErrMalformedResponse, len(payload), authResponseSize)
	}

	var nodeID [sha256.Size]byte
	copy(nodeID[:], payload[:sha256.Size])

	signature := bytes.Clone(payload[sha256.Size:])

	return nodeID, signature, nil
}

func rejectAuthentication(encoder *protocol.Encoder, cause error) error {
	if err := encoder.WriteFrame(protocol.TypeAuthFailed, nil); err != nil {
		return errors.Join(cause, fmt.Errorf("send authentication failure: %w", err))
	}
	return cause
}
