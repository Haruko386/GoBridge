package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Haruko386/GoBridge/internal/auth"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/protocol"
)

const (
	DefaultTimeout = 10 * time.Second

	pairingSignatureDomain = "gobridge/pairing/challenge/v1\x00"
)

var (
	ErrPairingFailed            = errors.New("pairing failed")
	ErrUnexpectedPairingMessage = errors.New("unexpected pairing message")
	ErrInvalidPairingSignature  = errors.New("invalid pairing signature")
	ErrPairingServerKeyMismatch = errors.New("pairing server public key mismatch")
)

func pairingSigningMessage(challenge auth.Challenge, name string, publicKey ed25519.PublicKey, code string) []byte {
	codeDigest := sha256.Sum256([]byte(code))
	nameBytes := []byte(name)

	var nameSize [2]byte
	binary.BigEndian.PutUint16(nameSize[:], uint16(len(nameBytes)))

	size := len(pairingSignatureDomain) + auth.ChallengeSize + ed25519.PublicKeySize + len(nameSize) + len(nameBytes) + sha256.Size

	message := make([]byte, 0, size)

	message = append(message, pairingSignatureDomain...)
	message = append(message, challenge[:]...)
	message = append(message, publicKey[:]...)
	message = append(message, nameSize[:]...)
	message = append(message, nameBytes...)
	message = append(message, codeDigest[:]...)

	return message
}

func AcceptServer(conn net.Conn, manager *Manager, serverIdentity identity.Identity, serverName string, store *peer.Store, timeout time.Duration) (peer.Peer, error) {
	if conn == nil {
		return peer.Peer{}, errors.New("pairing connection is nil")
	}

	if manager == nil {
		return peer.Peer{}, errors.New("pairing manager is nil")
	}

	if store == nil {
		return peer.Peer{}, errors.New("pairing store is nil")
	}

	if timeout <= 0 {
		return peer.Peer{}, errors.New("pairing timeout is invalid")
	}

	if err := serverIdentity.Validate(); err != nil {
		return peer.Peer{}, fmt.Errorf("validate server identity: %w", err)
	}

	if err := validatePeerName(serverName); err != nil {
		return peer.Peer{}, fmt.Errorf("validate server name: %w", err)
	}

	serverPublicKey := serverIdentity.PublicKey()

	if !bytes.Equal(serverPublicKey, manager.serverPublicKey) {
		return peer.Peer{}, ErrPairingServerKeyMismatch
	}

	responsePayload, err := encodeResponse(response{
		name:      serverName,
		publicKey: serverPublicKey,
	})
	if err != nil {
		return peer.Peer{}, fmt.Errorf("encode response: %w", err)
	}

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return peer.Peer{}, fmt.Errorf("set connection deadline: %w", err)
	}

	defer func() {
		_ = conn.SetDeadline(time.Time{})
	}()

	encoder := protocol.NewEncoder(conn)
	decoder := protocol.NewDecoder(conn)

	challenge, err := auth.GenerateChallenge()
	if err != nil {
		return peer.Peer{}, fmt.Errorf("generate challenge: %w", err)
	}

	if err := encoder.WriteFrame(protocol.TypePairChallenge, challenge.Bytes()); err != nil {
		return peer.Peer{}, fmt.Errorf("send pairing challenge: %w", err)
	}

	frame, err := decoder.ReadFrame()
	if err != nil {
		return peer.Peer{}, fmt.Errorf("read frame: %w", err)
	}

	if frame.Type != protocol.TypePairRequest {
		cause := fmt.Errorf(
			"%w: got %s, want %s",
			ErrUnexpectedPairingMessage,
			frame.Type,
			protocol.TypePairRequest,
		)

		return peer.Peer{}, rejectPairing(encoder, cause)
	}

	requestValue, err := decodeRequest(frame.Payload)
	if err != nil {
		return peer.Peer{}, rejectPairing(encoder, fmt.Errorf("decode request: %w", err))
	}

	signingMessage := pairingSigningMessage(challenge, requestValue.name, requestValue.publicKey, requestValue.code)
	if !ed25519.Verify(requestValue.publicKey, signingMessage, requestValue.signature) {
		return peer.Peer{}, rejectPairing(encoder, ErrInvalidPairingSignature)
	}

	code, err := Parse(requestValue.code)
	if err != nil {
		return peer.Peer{}, rejectPairing(encoder, err)
	}

	if err := manager.Consume(code); err != nil {
		return peer.Peer{}, rejectPairing(encoder, err)
	}

	clientPeer, err := peer.New(requestValue.name, requestValue.publicKey)
	if err != nil {
		return peer.Peer{}, rejectPairing(encoder, err)
	}

	if err := store.Add(clientPeer); err != nil {
		return peer.Peer{}, rejectPairing(encoder, fmt.Errorf("store paired client: %w", err))
	}

	if err := encoder.WriteFrame(protocol.TypePairOK, responsePayload); err != nil {
		return peer.Peer{}, fmt.Errorf("send pairing response: %w", err)
	}

	return clientPeer, nil
}

func PairClient(conn net.Conn, code Code, clientIdentity identity.Identity, clientName string, store *peer.Store, timeout time.Duration) (peer.Peer, error) {
	if conn == nil {
		return peer.Peer{}, errors.New("pairing connection is nil")
	}

	if store == nil {
		return peer.Peer{}, errors.New("peer store is nil")
	}

	if timeout <= 0 {
		return peer.Peer{}, errors.New("pairing timeout is invalid")
	}

	if err := clientIdentity.Validate(); err != nil {
		return peer.Peer{}, fmt.Errorf("validate client identity: %w", err)
	}

	if err := validatePeerName(clientName); err != nil {
		return peer.Peer{}, fmt.Errorf("validate client name: %w", err)
	}

	parsedCode, err := Parse(code.String())
	if err != nil {
		return peer.Peer{}, fmt.Errorf("parse code: %w", err)
	}

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return peer.Peer{}, fmt.Errorf("set connection deadline: %w", err)
	}

	defer func() {
		_ = conn.SetDeadline(time.Time{})
	}()

	encoder := protocol.NewEncoder(conn)
	decoder := protocol.NewDecoder(conn)

	frame, err := decoder.ReadFrame()
	if err != nil {
		return peer.Peer{}, fmt.Errorf("read frame: %w", err)
	}

	if frame.Type != protocol.TypePairChallenge {
		return peer.Peer{}, fmt.Errorf("%w: got %s, want %s", ErrUnexpectedPairingMessage, frame.Type, protocol.TypePairChallenge)
	}

	challenge, err := auth.ParseChallenge(frame.Payload)
	if err != nil {
		return peer.Peer{}, fmt.Errorf("parse pairing challenge: %w", err)
	}

	clientPublicKey := clientIdentity.PublicKey()
	signingMessage := pairingSigningMessage(challenge, clientName, clientPublicKey, parsedCode.String())

	signature := ed25519.Sign(clientIdentity.PrivateKey(), signingMessage)

	requestPayload, err := encodeRequest(request{
		name:      clientName,
		publicKey: clientPublicKey,
		signature: signature,
		code:      parsedCode.String(),
	})
	if err != nil {
		return peer.Peer{}, fmt.Errorf("encode request: %w", err)
	}

	if err := encoder.WriteFrame(protocol.TypePairRequest, requestPayload); err != nil {
		return peer.Peer{}, fmt.Errorf("send pairing request: %w", err)
	}

	result, err := decoder.ReadFrame()
	if err != nil {
		return peer.Peer{}, fmt.Errorf("read pairing result: %w", err)
	}

	switch result.Type {
	case protocol.TypePairFailed:
		if len(result.Payload) != 0 {
			return peer.Peer{}, fmt.Errorf("%w: PAIR_FAILED payload must be empty", ErrUnexpectedPairingMessage)
		}
		return peer.Peer{}, ErrPairingFailed

	case protocol.TypePairOK:
		// 继续处理。
	default:
		return peer.Peer{}, fmt.Errorf("%w: got %s, want PAIR_OK or PAIR_FAILED", ErrUnexpectedPairingMessage, result.Type)
	}

	responseValue, err := decodeResponse(result.Payload)
	if err != nil {
		return peer.Peer{}, err
	}

	if !bytes.Equal(responseValue.publicKey, parsedCode.ServerPublicKey()) {
		return peer.Peer{}, ErrPairingServerKeyMismatch
	}

	serverPeer, err := peer.New(responseValue.name, responseValue.publicKey)
	if err != nil {
		return peer.Peer{}, fmt.Errorf("create server peer: %w", err)
	}

	if err := store.Add(serverPeer); err != nil {
		return peer.Peer{}, fmt.Errorf("store paired server: %w", err)
	}

	return serverPeer, nil
}

func rejectPairing(encoder *protocol.Encoder, cause error) error {
	if err := encoder.WriteFrame(protocol.TypePairFailed, nil); err != nil {
		return errors.Join(ErrPairingFailed, cause, fmt.Errorf("send pairing failure: %w", err))
	}
	return errors.Join(ErrPairingFailed, cause)
}
