package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/transport"
)

type SessionHandler func(context.Context, Session) error
type ErrorHandler func(error)

type Server struct {
	listener    net.Listener
	tlsConfig   *tls.Config
	lookup      transport.PublicKeyLookup
	registry    *Registry
	authTimeout time.Duration
	handler     SessionHandler
	reportError ErrorHandler
}

func NewServer(
	listener net.Listener,
	nodeIdentity identity.Identity,
	lookup transport.PublicKeyLookup,
	registry *Registry,
	authTimeout time.Duration,
	handler SessionHandler,
	reportError ErrorHandler,
) (*Server, error) {
	if listener == nil {
		return nil, fmt.Errorf("server listener is nil")
	}

	if err := nodeIdentity.Validate(); err != nil {
		return nil, fmt.Errorf("node identity validate failed: %w", err)
	}

	if lookup == nil {
		return nil, fmt.Errorf("server lookup is nil")
	}

	if registry == nil {
		return nil, fmt.Errorf("server registry is nil")
	}

	if authTimeout <= 0 {
		return nil, fmt.Errorf("server auth timeout is invalid")
	}

	if handler == nil {
		return nil, fmt.Errorf("server handler is nil")
	}

	if reportError == nil {
		reportError = func(err error) {}
	}

	tlsConfig, err := transport.NewServerTLSConfig(nodeIdentity)
	if err != nil {
		return nil, err
	}

	return &Server{
		listener:    listener,
		tlsConfig:   tlsConfig,
		lookup:      lookup,
		registry:    registry,
		authTimeout: authTimeout,
		handler:     handler,
		reportError: reportError,
	}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	if ctx == nil {
		return errors.New("server context is nil")
	}

	runCtx, cancel := context.WithCancel(ctx)

	var workers sync.WaitGroup

	defer func() {
		cancel()
		_ = s.listener.Close()
		workers.Wait()
	}()

	stopAccept := context.AfterFunc(runCtx, func() {
		_ = s.listener.Close()
	})
	defer stopAccept()

	for {
		rawConn, err := s.listener.Accept()
		if err != nil {
			if runCtx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		workers.Add(1)
		go func() {
			defer workers.Done()
			s.handleConnection(runCtx, rawConn)
		}()
	}
}

func (s *Server) handleConnection(ctx context.Context, rawConn net.Conn) {
	stopConn := context.AfterFunc(ctx, func() {
		_ = rawConn.Close()
	})
	defer stopConn()

	session, err := transport.AcceptServerSession(ctx, rawConn, s.tlsConfig, s.lookup, s.authTimeout)
	if err != nil {
		if ctx.Err() == nil {
			s.reportError(fmt.Errorf("accept server session: %w", err))
		}
		return
	}

	defer session.Close()

	registration, err := s.registry.Register(session)
	if registration == nil {
		if err != nil {
			s.reportError(fmt.Errorf("register session: %w", err))
		} else {
			s.reportError(errors.New("register session returned nil registration"))
		}
		return
	}

	defer registration.Remove()

	if err != nil {
		s.reportError(fmt.Errorf("register session: %w", err))
	}

	if err := s.handler(ctx, session); err != nil {
		s.reportError(fmt.Errorf("handle session: %w", err))
	}
}
