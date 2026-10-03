package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/transport"
)

var (
	ErrNotPaired          = errors.New("client is not paired")
	ErrServerPeerDisabled = errors.New("server peer is disabled")
	ErrUnexpectedServer   = errors.New("connected server identity is unexpected")
)

type Connector struct {
	address      string
	serverNodeID string
	nodeIdentity identity.Identity
	peers        *peer.Store
	authTimeout  time.Duration
}

func NewConnector(cfg config.Config, nodeIdentity identity.Identity, peers *peer.Store, authTimeout time.Duration) (*Connector, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate client config: %w", err)
	}
	if cfg.Role != config.RoleClient {
		return nil, fmt.Errorf("invalid connector role %q: want %q", cfg.Role, config.RoleClient)
	}

	if cfg.Client == nil {
		return nil, errors.New("client configuration is missing")
	}

	if err := nodeIdentity.Validate(); err != nil {
		return nil, fmt.Errorf("validate client identity: %w", err)
	}

	if peers == nil {
		return nil, errors.New("peer store is nil")
	}

	if authTimeout <= 0 {
		return nil, errors.New("authentication timeout must be positive")
	}

	if cfg.Client.ServerAddress == "" ||
		cfg.Client.ServerNodeID == "" {
		return nil, ErrNotPaired
	}

	serverPeer, err := peers.Get(
		cfg.Client.ServerNodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("load paired server %s: %w", cfg.Client.ServerNodeID, err)
	}

	if !serverPeer.Enabled {
		return nil, fmt.Errorf("%w: %s", ErrServerPeerDisabled, cfg.Client.ServerNodeID)
	}

	return &Connector{
		address:      cfg.Client.ServerAddress,
		serverNodeID: cfg.Client.ServerNodeID,
		nodeIdentity: nodeIdentity,
		peers:        peers,
		authTimeout:  authTimeout,
	}, nil
}

func (c *Connector) Connect(ctx context.Context) (*transport.Session, error) {
	if ctx == nil {
		return nil, errors.New("connection context is nil")
	}
	if err := c.peers.Reload(); err != nil {
		return nil, fmt.Errorf("reload peer store: %w", err)
	}

	serverPeer, err := c.peers.Get(c.serverNodeID)
	if err != nil {
		return nil, fmt.Errorf("get peer %s: %w", c.serverNodeID, err)
	}

	if !serverPeer.Enabled {
		return nil, fmt.Errorf("%w: %s", ErrServerPeerDisabled, c.serverNodeID)
	}

	tlsConfig, err := transport.NewClientTLSConfig(serverPeer.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("create client tls config: %w", err)
	}

	session, err := transport.DialClientSession(
		ctx,
		c.address,
		tlsConfig,
		c.nodeIdentity,
		c.authTimeout,
	)
	if err != nil {
		return nil, fmt.Errorf("connect to server %s: %w", c.address, err)
	}

	if session.PeerNodeID() != c.serverNodeID {
		closeErr := session.Close()

		mismatchErr := fmt.Errorf("%w: got %s, want %s", ErrUnexpectedServer, session.PeerNodeID(), c.serverNodeID)
		if closeErr != nil {
			return nil, errors.Join(mismatchErr, closeErr)
		}
		return nil, mismatchErr
	}
	return session, nil
}

func (c *Connector) Address() string {
	return c.address
}

func (c *Connector) ServerNodeID() string {
	return c.serverNodeID
}
