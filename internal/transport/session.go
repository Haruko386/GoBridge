package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/protocol"
)

type Session struct {
	conn       *tls.Conn
	encoder    *protocol.Encoder
	decoder    *protocol.Decoder
	peerNodeID string
}

func newSession(conn *tls.Conn, peerNodeID string) *Session {
	return &Session{
		conn:       conn,
		encoder:    protocol.NewEncoder(conn),
		decoder:    protocol.NewDecoder(conn),
		peerNodeID: peerNodeID,
	}
}

func AcceptServerSession(ctx context.Context, rawConn net.Conn, tlsConfig *tls.Config, lookup PublicKeyLookup, authenticationTimeout time.Duration) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("server session context is nil")
	}
	if rawConn == nil {
		return nil, errors.New("server connection is nil")
	}
	if tlsConfig == nil {
		return nil, errors.New("server TLS config is nil")
	}
	if lookup == nil {
		return nil, errors.New("public key lookup is nil")
	}
	if authenticationTimeout <= 0 {
		return nil, errors.New("authentication timeout must be positive")
	}

	tlsConn := tls.Server(rawConn, tlsConfig.Clone())

	complete := false

	defer func() {
		if !complete {
			_ = tlsConn.Close()
		}
	}()

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("server handshake failed: %w", err)
	}

	peerNodeID, err := AuthenticateServer(tlsConn, lookup, authenticationTimeout)
	if err != nil {
		return nil, fmt.Errorf("authenticate client: %w", err)
	}

	session := newSession(tlsConn, peerNodeID)
	complete = true
	return session, nil
}

func DialClientSession(ctx context.Context, address string, tlsConfig *tls.Config, nodeIdentity identity.Identity, authenticationTimeout time.Duration) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("client session context is nil")
	}
	if address == "" {
		return nil, errors.New("server address is empty")
	}
	if tlsConfig == nil {
		return nil, errors.New("client TLS config is nil")
	}
	if authenticationTimeout <= 0 {
		return nil, errors.New("authentication timeout must be positive")
	}

	if err := nodeIdentity.Validate(); err != nil {
		return nil, fmt.Errorf("validate client identity: %w", err)
	}

	dialer := net.Dialer{}

	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial server %s: %w", address, err)
	}

	tlsConn := tls.Client(rawConn, tlsConfig.Clone())
	complete := false

	defer func() {
		if !complete {
			_ = tlsConn.Close()
		}
	}()

	if err = tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("client TLS handshake: %w", err)
	}

	if err = AuthenticateClient(tlsConn, nodeIdentity, authenticationTimeout); err != nil {
		return nil, fmt.Errorf("authenticate with server: %w", err)
	}

	peerNodeID, err := serverNodeID(tlsConn)
	if err != nil {
		return nil, err
	}

	session := newSession(tlsConn, peerNodeID)
	complete = true
	return session, nil
}

func (s *Session) PeerNodeID() string {
	return s.peerNodeID
}

func (s *Session) WriteFrame(messageType protocol.MessageType, payload []byte) error {
	if err := s.encoder.WriteFrame(messageType, payload); err != nil {
		return fmt.Errorf("write session frame: %w", err)
	}
	return nil
}

func (s *Session) ReadFrame() (protocol.Frame, error) {
	frame, err := s.decoder.ReadFrame()
	if err != nil {
		return protocol.Frame{}, fmt.Errorf("read session frame: %w", err)
	}
	return frame, nil
}

func (s *Session) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *Session) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *Session) Close() error {
	if err := s.conn.Close(); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	return nil
}

func serverNodeID(conn *tls.Conn) (string, error) {
	state := conn.ConnectionState()

	if len(state.PeerCertificates) == 0 {
		return "", errors.New("server TLS certificate is missing")
	}

	publicKey, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("server TLS certificate does not use Ed25519")
	}

	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:]), nil
}
