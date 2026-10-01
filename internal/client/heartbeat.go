package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

var (
	ErrHeartbeatTimeout         = errors.New("heartbeat timeout")
	ErrUnexpectedHeartbeatFrame = errors.New("unexpected heartbeat frame")
	ErrInvalidHeartbeat         = errors.New("invalid heartbeat")
)

type HeartbeatSession interface {
	WriteFrame(protocol.MessageType, []byte) error
	ReadFrame() (protocol.Frame, error)
	Close() error
}

type heartbeatReadResult struct {
	frame protocol.Frame
	err   error
}

type closeOnceHeartbeatSession struct {
	HeartbeatSession
	closeOnce sync.Once
	closeErr  error
}

func (s *closeOnceHeartbeatSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.HeartbeatSession.Close()
	})
	return s.closeErr
}

func RunHeartbeat(ctx context.Context, session HeartbeatSession, interval, timeout time.Duration) error {
	if ctx == nil {
		return errors.New("heartbeat context is nil")
	}
	if session == nil {
		return errors.New("heartbeat session is nil")
	}
	if interval <= 0 {
		return errors.New("heartbeat interval must be positive")
	}
	if timeout <= 0 {
		return errors.New("heartbeat timeout must be positive")
	}

	session = &closeOnceHeartbeatSession{HeartbeatSession: session}

	if ctx.Err() != nil {
		_ = session.Close()
		return nil
	}

	results := make(chan heartbeatReadResult, 1)
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)

	// get PONG
	go func() {
		defer wg.Done()

		for {
			frame, err := session.ReadFrame()
			result := heartbeatReadResult{frame: frame, err: err}

			select {
			case results <- result:
			case <-done:
				return
			}

			if err != nil {
				return
			}
		}
	}()

	defer func() {
		close(done)
		_ = session.Close()
		wg.Wait()
	}()

	for {
		if err := writeHeartbeatPing(ctx, session, timeout); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("write heartbeat PING: %w", err)
		}

		responseTimer := time.NewTimer(timeout)

		select {
		case <-ctx.Done():
			stopAndDrainTimer(responseTimer)
			return nil
		case <-responseTimer.C:
			return fmt.Errorf("%w: after %s", ErrHeartbeatTimeout, timeout)
		case result := <-results:
			stopAndDrainTimer(responseTimer)
			if result.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("read heartbeat response: %w", result.err)
			}

			if result.frame.Type != protocol.TypePong {
				return fmt.Errorf("%w: got %s, want %s", ErrUnexpectedHeartbeatFrame, result.frame.Type, protocol.TypePong)
			}

			if len(result.frame.Payload) != 0 {
				return fmt.Errorf("%w: PONG payload must be empty", ErrInvalidHeartbeat)
			}
		}

		intervalTimer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			stopAndDrainTimer(intervalTimer)
			return nil
		case <-intervalTimer.C:
		case result := <-results:
			stopAndDrainTimer(intervalTimer)
			if result.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("read session while waiting for next heartbeat: %w", result.err)
			}
			return fmt.Errorf("%w: received %s without an outstanding PING", ErrUnexpectedHeartbeatFrame, result.frame.Type)
		}
	}
}

func writeHeartbeatPing(ctx context.Context, session HeartbeatSession, timeout time.Duration) error {
	result := make(chan error, 1)
	go func() {
		result <- session.WriteFrame(protocol.TypePing, nil)
	}()

	timer := time.NewTimer(timeout)
	defer stopAndDrainTimer(timer)

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		_ = session.Close()
		<-result
		return ctx.Err()
	case <-timer.C:
		_ = session.Close()
		<-result
		return fmt.Errorf("%w: after %s", ErrHeartbeatTimeout, timeout)
	}
}

func stopAndDrainTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
