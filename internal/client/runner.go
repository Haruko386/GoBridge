package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/transport"
	"github.com/Haruko386/GoBridge/internal/tunnel"
)

type Runner struct {
	connect           connectSessionFunc
	runSession        runSessionFunc
	waitRetry         waitRetryFunc
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	minBackoff        time.Duration
	maxBackoff        time.Duration
	reportError       func(error)
}

type connectSessionFunc func(context.Context) (HeartbeatSession, error)

type runSessionFunc func(context.Context, HeartbeatSession, time.Duration, time.Duration) error

type waitRetryFunc func(context.Context, time.Duration) bool

func NewRunner(connector *Connector, heartbeatInterval time.Duration, heartbeatTimeout time.Duration, minBackoff time.Duration, maxBackoff time.Duration, reportError func(error)) (*Runner, error) {
	if connector == nil {
		return nil, errors.New("runner connector is nil")
	}

	return newRunner(
		func(ctx context.Context) (HeartbeatSession, error) {
			return connector.Connect(ctx)
		},
		RunHeartbeat,
		heartbeatInterval,
		heartbeatTimeout,
		minBackoff,
		maxBackoff,
		reportError,
	)
}

func NewTunnelRunner(connector *Connector, forwarder *ProxyForwarder, heartbeatInterval, heartbeatTimeout, minBackoff, maxBackoff time.Duration, reportError func(error)) (*Runner, error) {
	if connector == nil {
		return nil, errors.New("runner connector is nil")
	}
	if forwarder == nil {
		return nil, errors.New("runner proxy forwarder is nil")
	}
	if reportError == nil {
		reportError = func(error) {}
	}

	runSession := func(ctx context.Context, session HeartbeatSession, interval, timeout time.Duration) error {
		sessionCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		manager, err := tunnel.NewManager(session, tunnel.FirstClientStreamID, func(stream *tunnel.Stream) {
			if err := forwarder.Forward(sessionCtx, stream); err != nil && sessionCtx.Err() == nil {
				reportError(fmt.Errorf("forward proxy stream: %w", err))
			}
		})
		if err != nil {
			return fmt.Errorf("create client tunnel manager: %w", err)
		}

		runtime, err := tunnel.NewRuntime(session, manager, interval, timeout)
		if err != nil {
			_ = manager.Close()
			return fmt.Errorf("create client tunnel runtime: %w", err)
		}
		return runtime.Run(sessionCtx)
	}

	return newRunner(func(ctx context.Context) (HeartbeatSession, error) { return connector.Connect(ctx) }, runSession, heartbeatInterval, heartbeatTimeout, minBackoff, maxBackoff, reportError)
}

func newRunner(
	connect connectSessionFunc,
	runSession runSessionFunc,
	heartbeatInterval time.Duration,
	heartbeatTimeout time.Duration,
	minBackoff time.Duration,
	maxBackoff time.Duration,
	reportError func(error),
) (*Runner, error) {
	if connect == nil {
		return nil, errors.New("runner connect function is nil")
	}
	if runSession == nil {
		return nil, errors.New("runner session function is nil")
	}

	if heartbeatInterval <= 0 {
		return nil, fmt.Errorf("heartbeat interval should be positive: %d", heartbeatInterval)
	}
	if heartbeatTimeout <= 0 {
		return nil, fmt.Errorf("heartbeat timeout should be positive: %d", heartbeatTimeout)
	}
	if minBackoff <= 0 {
		return nil, fmt.Errorf("min backoff should be positive: %d", minBackoff)
	}
	if maxBackoff <= 0 {
		return nil, fmt.Errorf("max backoff should be positive: %d", maxBackoff)
	}
	if maxBackoff < minBackoff {
		return nil, fmt.Errorf("max backoff %s is less than min backoff %s", maxBackoff, minBackoff)
	}

	if reportError == nil {
		reportError = func(error) {}
	}

	return &Runner{
		connect:           connect,
		runSession:        runSession,
		waitRetry:         waitForRetry,
		heartbeatInterval: heartbeatInterval,
		heartbeatTimeout:  heartbeatTimeout,
		minBackoff:        minBackoff,
		maxBackoff:        maxBackoff,
		reportError:       reportError,
	}, nil
}

func (r *Runner) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runner context is nil")
	}

	backoff := r.minBackoff

	for {
		session, err := r.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			if isPermanentConnectionError(err) {
				return err
			}

			r.reportError(fmt.Errorf("connect to server: %w", err))

			if !r.waitRetry(ctx, backoff) {
				return nil
			}

			backoff = nextBackoff(backoff, r.maxBackoff)
			continue
		}

		backoff = r.minBackoff

		err = r.runSession(ctx, session, r.heartbeatInterval, r.heartbeatTimeout)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.reportError(fmt.Errorf("connection ended: %w", err))
		}
		if !r.waitRetry(ctx, backoff) {
			return nil
		}
	}
}

func isPermanentConnectionError(err error) bool {
	return errors.Is(err, ErrServerPeerDisabled) ||
		errors.Is(err, peer.ErrNotFound) ||
		errors.Is(err, ErrUnexpectedServer) ||
		errors.Is(err, transport.ErrServerIdentityMismatch)
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer stopAndDrainTimer(timer)

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum {
		return maximum
	}

	// 防止 current * 2 溢出。
	if current > maximum-current {
		return maximum
	}

	next := current * 2
	if next > maximum {
		return maximum
	}

	return next
}
