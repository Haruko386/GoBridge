package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/pairing"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func AcceptServerPairing(
	ctx context.Context,
	rawConn net.Conn,
	tlsConfig *tls.Config,
	manager *pairing.Manager,
	serverIdentity identity.Identity,
	serverName string,
	store *peer.Store,
	pairingTimeout time.Duration,
) (peer.Peer, error) {
	if ctx == nil {
		return peer.Peer{}, errors.New("server pairing context is nil")
	}

	if rawConn == nil {
		return peer.Peer{}, errors.New("server pairing connection is nil")
	}

	if tlsConfig == nil {
		return peer.Peer{}, errors.New("server TLS config is nil")
	}

	if manager == nil {
		return peer.Peer{}, errors.New("pairing manager is nil")
	}

	if store == nil {
		return peer.Peer{}, errors.New("peer store is nil")
	}

	if pairingTimeout <= 0 {
		return peer.Peer{}, errors.New("pairing timeout must be positive")
	}

	if err := serverIdentity.Validate(); err != nil {
		return peer.Peer{}, fmt.Errorf("validate server identity: %w", err)
	}

	tlsConn := tls.Server(rawConn, tlsConfig.Clone())

	defer func() {
		_ = tlsConn.Close()
	}()

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return peer.Peer{}, fmt.Errorf("server pairing TLS handshake failed: %w", err)
	}

	pairedPeer, err := pairing.AcceptServer(tlsConn, manager, serverIdentity, serverName, store, pairingTimeout)
	if err != nil {
		return peer.Peer{}, fmt.Errorf("accept server pairing: %w", err)
	}

	return pairedPeer, nil
}

func DialClientPairing(
	ctx context.Context,
	address string,
	code pairing.Code,
	clientIdentity identity.Identity,
	clientName string,
	store *peer.Store,
	pairingTimeout time.Duration,
) (peer.Peer, error) {
	if ctx == nil {
		return peer.Peer{}, errors.New("client pairing context is nil")
	}

	if address == "" {
		return peer.Peer{}, errors.New("server address is empty")
	}

	if store == nil {
		return peer.Peer{}, errors.New("peer store is nil")
	}

	if pairingTimeout <= 0 {
		return peer.Peer{}, errors.New("pairing timeout must be positive")
	}

	if err := clientIdentity.Validate(); err != nil {
		return peer.Peer{}, fmt.Errorf("validate client identity: %w", err)
	}

	parsedCode, err := pairing.Parse(code.String())
	if err != nil {
		return peer.Peer{}, fmt.Errorf("parse client pairing code: %w", err)
	}

	tlsConfig, err := NewClientTLSConfig(parsedCode.ServerPublicKey())
	if err != nil {
		return peer.Peer{}, fmt.Errorf("create client pairing tls config: %w", err)
	}

	dialer := net.Dialer{}

	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return peer.Peer{}, fmt.Errorf("dial pairing server %s: %w", address, err)
	}

	tlsConn := tls.Client(rawConn, tlsConfig)

	defer func() {
		_ = tlsConn.Close()
	}()

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return peer.Peer{}, fmt.Errorf("client pairing TLS handshake failed: %w", err)
	}

	pairedPeer, err := pairing.PairClient(tlsConn, parsedCode, clientIdentity, clientName, store, pairingTimeout)
	if err != nil {
		return peer.Peer{}, fmt.Errorf("pair client: %w", err)
	}

	return pairedPeer, nil
}
