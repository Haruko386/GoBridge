package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Haruko386/GoBridge/internal/proxy"
	"github.com/Haruko386/GoBridge/internal/tunnel"
)

func NewTunnelHandler(tunnels *TunnelRegistry, heartbeatTimeout time.Duration) (SessionHandler, error) {
	if tunnels == nil {
		return nil, errors.New("server tunnel registry is nil")
	}
	if heartbeatTimeout <= 0 {
		return nil, errors.New("server heartbeat timeout must be positive")
	}

	return func(ctx context.Context, session Session) error {
		manager, err := tunnel.NewManager(session, tunnel.FirstServerStreamID, nil)
		if err != nil {
			return fmt.Errorf("create server tunnel manager: %w", err)
		}

		registration, err := tunnels.Register(session.PeerNodeID(), func(openCtx context.Context) (proxy.Endpoint, error) {
			return manager.Open(openCtx)
		})
		if err != nil {
			_ = manager.Close()
			return fmt.Errorf("register server tunnel: %w", err)
		}
		defer registration.Remove()

		runtime, err := tunnel.NewRuntime(session, manager, 0, heartbeatTimeout)
		if err != nil {
			_ = manager.Close()
			return fmt.Errorf("create server tunnel runtime: %w", err)
		}
		return runtime.Run(ctx)
	}, nil
}
