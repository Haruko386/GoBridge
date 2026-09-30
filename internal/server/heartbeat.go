package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

var (
	ErrUnexpectedSessionFrame = errors.New("unexpected session frame")
	ErrInvalidHeartbeat       = errors.New("invalid heartbeat")
)

func HandleHeartbeatSession(ctx context.Context, session Session) error {
	// validate
	if ctx == nil {
		return errors.New("heartbeat context is nil")
	}
	if session == nil {
		return errors.New("session is nil")
	}

	for {
		frame, err := session.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read heartbeat frame: %w", err)
		}
		// check frame info
		if frame.Type != protocol.TypePing {
			return fmt.Errorf("%w: got %s, want %s", ErrUnexpectedSessionFrame, frame.Type, protocol.TypePing)
		}

		if len(frame.Payload) != 0 {
			return fmt.Errorf("%w: PING payload should be empty", ErrInvalidHeartbeat)
		}
		// pong
		if err := session.WriteFrame(protocol.TypePong, nil); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("write heartbeat frame: %w", err)
		}
	}
}
