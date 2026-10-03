package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Haruko386/GoBridge/internal/proxy"
)

type ProxyServer struct {
	listener    net.Listener
	tunnels     *TunnelRegistry
	reportError ErrorHandler
}

func NewProxyServer(listener net.Listener, tunnels *TunnelRegistry, reportError ErrorHandler) (*ProxyServer, error) {
	if listener == nil {
		return nil, errors.New("proxy listener is nil")
	}
	if tunnels == nil {
		return nil, errors.New("proxy tunnel registry is nil")
	}
	if reportError == nil {
		reportError = func(error) {}
	}

	return &ProxyServer{listener: listener, tunnels: tunnels, reportError: reportError}, nil
}

func (s *ProxyServer) Serve(ctx context.Context) error {
	if ctx == nil {
		return errors.New("proxy server context is nil")
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

	var retryDelay time.Duration
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if runCtx.Err() != nil {
				return nil
			}

			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				retryDelay = nextAcceptRetryDelay(retryDelay)
				timer := time.NewTimer(retryDelay)
				select {
				case <-runCtx.Done():
					stopServerTimer(timer)
					return nil
				case <-timer.C:
				}
				continue
			}

			return fmt.Errorf("accept proxy connection: %w", err)
		}
		retryDelay = 0

		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := s.handleConnection(runCtx, connection); err != nil && runCtx.Err() == nil {
				s.reportError(err)
			}
		}()
	}
}

func (s *ProxyServer) handleConnection(ctx context.Context, connection net.Conn) error {
	defer connection.Close()

	nodeID, open, err := s.tunnels.Single()
	if err != nil {
		return err
	}

	stream, err := open(ctx)
	if err != nil {
		return fmt.Errorf("open stream to proxy provider %s: %w", nodeID, err)
	}
	if stream == nil {
		return fmt.Errorf("open stream to proxy provider %s returned nil", nodeID)
	}
	defer stream.Close()

	if err := proxy.Bridge(ctx, connection, stream); err != nil {
		return fmt.Errorf("bridge proxy connection through %s: %w", nodeID, err)
	}
	return nil
}

func nextAcceptRetryDelay(current time.Duration) time.Duration {
	if current == 0 {
		return initialAcceptRetryDelay
	}
	if current >= maxAcceptRetryDelay/2 {
		return maxAcceptRetryDelay
	}
	return current * 2
}

func stopServerTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
