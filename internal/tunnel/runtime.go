package tunnel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

var (
	ErrHeartbeatTimeout       = errors.New("heartbeat timeout")
	ErrInvalidHeartbeat       = errors.New("invalid heartbeat")
	ErrUnexpectedSessionFrame = errors.New("unexpected session frame")
)

type Session interface {
	WriteFrame(protocol.MessageType, []byte) error
	ReadFrame() (protocol.Frame, error)
	Close() error
}

type Runtime struct {
	session           *closeOnceSession
	manager           *Manager
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration

	heartbeatMu sync.Mutex
	waitingPong bool
	pong        chan struct{}
}

type closeOnceSession struct {
	Session
	closeOnce sync.Once
	closeErr  error
}

func (s *closeOnceSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.Session.Close()
	})
	return s.closeErr
}

func NewRuntime(session Session, manager *Manager, heartbeatInterval, heartbeatTimeout time.Duration) (*Runtime, error) {
	if session == nil {
		return nil, errors.New("tunnel runtime session is nil")
	}
	if manager == nil {
		return nil, errors.New("tunnel runtime manager is nil")
	}
	if heartbeatInterval < 0 {
		return nil, errors.New("heartbeat interval must not be negative")
	}
	if heartbeatTimeout <= 0 {
		return nil, errors.New("heartbeat timeout must be positive")
	}

	return &Runtime{
		session:           &closeOnceSession{Session: session},
		manager:           manager,
		heartbeatInterval: heartbeatInterval,
		heartbeatTimeout:  heartbeatTimeout,
		pong:              make(chan struct{}, 1),
	}, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("tunnel runtime context is nil")
	}

	runCtx, cancel := context.WithCancel(ctx)
	results := make(chan error, 2)

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		results <- r.readLoop(runCtx)
	}()

	if r.heartbeatInterval > 0 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- r.heartbeatLoop(runCtx)
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-results:
	}

	cancel()
	_ = r.session.Close()
	_ = r.manager.Close()
	workers.Wait()

	if ctx.Err() != nil {
		return nil
	}
	return runErr
}

func (r *Runtime) readLoop(ctx context.Context) error {
	for {
		frame, err := r.session.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read tunnel session frame: %w", err)
		}

		switch frame.Type {
		case protocol.TypePing:
			if len(frame.Payload) != 0 {
				return fmt.Errorf("%w: PING payload must be empty", ErrInvalidHeartbeat)
			}
			if err := r.writeFrame(ctx, protocol.TypePong, nil); err != nil {
				return fmt.Errorf("write heartbeat PONG: %w", err)
			}

		case protocol.TypePong:
			if len(frame.Payload) != 0 {
				return fmt.Errorf("%w: PONG payload must be empty", ErrInvalidHeartbeat)
			}
			if err := r.acceptPong(); err != nil {
				return err
			}

		case protocol.TypeOpenStream, protocol.TypeOpenOK, protocol.TypeOpenFailed, protocol.TypeStreamData, protocol.TypeCloseStream:
			if err := r.manager.HandleFrame(frame); err != nil {
				return fmt.Errorf("handle %s frame: %w", frame.Type, err)
			}

		default:
			return fmt.Errorf("%w: %s", ErrUnexpectedSessionFrame, frame.Type)
		}
	}
}

func (r *Runtime) heartbeatLoop(ctx context.Context) error {
	for {
		if err := r.startHeartbeat(); err != nil {
			return err
		}

		if err := r.writeFrame(ctx, protocol.TypePing, nil); err != nil {
			r.cancelHeartbeat()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("write heartbeat PING: %w", err)
		}

		responseTimer := time.NewTimer(r.heartbeatTimeout)
		select {
		case <-ctx.Done():
			stopRuntimeTimer(responseTimer)
			r.cancelHeartbeat()
			return nil
		case <-responseTimer.C:
			r.cancelHeartbeat()
			return fmt.Errorf("%w: after %s", ErrHeartbeatTimeout, r.heartbeatTimeout)
		case <-r.pong:
			stopRuntimeTimer(responseTimer)
		}

		intervalTimer := time.NewTimer(r.heartbeatInterval)
		select {
		case <-ctx.Done():
			stopRuntimeTimer(intervalTimer)
			return nil
		case <-intervalTimer.C:
		}
	}
}

func (r *Runtime) writeFrame(ctx context.Context, messageType protocol.MessageType, payload []byte) error {
	result := make(chan error, 1)
	go func() {
		result <- r.session.WriteFrame(messageType, payload)
	}()

	timer := time.NewTimer(r.heartbeatTimeout)
	defer stopRuntimeTimer(timer)

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		_ = r.session.Close()
		<-result
		return ctx.Err()
	case <-timer.C:
		_ = r.session.Close()
		<-result
		return fmt.Errorf("%w: writing %s after %s", ErrHeartbeatTimeout, messageType, r.heartbeatTimeout)
	}
}

func (r *Runtime) startHeartbeat() error {
	r.heartbeatMu.Lock()
	defer r.heartbeatMu.Unlock()

	if r.waitingPong {
		return errors.New("heartbeat is already waiting for PONG")
	}

	r.waitingPong = true
	return nil
}

func (r *Runtime) acceptPong() error {
	r.heartbeatMu.Lock()
	if !r.waitingPong {
		r.heartbeatMu.Unlock()
		return fmt.Errorf("%w: PONG without an outstanding PING", ErrUnexpectedSessionFrame)
	}
	r.waitingPong = false
	r.heartbeatMu.Unlock()

	select {
	case r.pong <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("%w: duplicate PONG", ErrUnexpectedSessionFrame)
	}
}

func (r *Runtime) cancelHeartbeat() {
	r.heartbeatMu.Lock()
	r.waitingPong = false
	r.heartbeatMu.Unlock()
}

func stopRuntimeTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
