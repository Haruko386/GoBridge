package server

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

func TestHandleHeartbeatSessionRepliesToPings(t *testing.T) {
	session := &heartbeatTestSession{
		frames: []protocol.Frame{
			{Type: protocol.TypePing},
			{Type: protocol.TypePing},
		},
		readErr: io.EOF,
	}

	err := HandleHeartbeatSession(context.Background(), session)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("HandleHeartbeatSession() error = %v, want wrapped EOF", err)
	}
	want := []protocol.Frame{
		{Type: protocol.TypePong},
		{Type: protocol.TypePong},
	}
	if !reflect.DeepEqual(session.writes, want) {
		t.Fatalf("written frames = %#v, want %#v", session.writes, want)
	}
}

func TestHandleHeartbeatSessionValidatesArguments(t *testing.T) {
	if err := HandleHeartbeatSession(nil, &heartbeatTestSession{}); err == nil {
		t.Fatal("HandleHeartbeatSession(nil context) error = nil")
	}
	if err := HandleHeartbeatSession(context.Background(), nil); err == nil {
		t.Fatal("HandleHeartbeatSession(nil session) error = nil")
	}
}

func TestHandleHeartbeatSessionRejectsUnexpectedFrames(t *testing.T) {
	session := &heartbeatTestSession{
		frames: []protocol.Frame{{Type: protocol.TypePong}},
	}

	err := HandleHeartbeatSession(context.Background(), session)
	if !errors.Is(err, ErrUnexpectedSessionFrame) {
		t.Fatalf("HandleHeartbeatSession() error = %v, want ErrUnexpectedSessionFrame", err)
	}
	if len(session.writes) != 0 {
		t.Fatalf("written frames = %v, want none", session.writes)
	}
}

func TestHandleHeartbeatSessionRejectsPingPayload(t *testing.T) {
	session := &heartbeatTestSession{
		frames: []protocol.Frame{{Type: protocol.TypePing, Payload: []byte("unexpected")}},
	}

	err := HandleHeartbeatSession(context.Background(), session)
	if !errors.Is(err, ErrInvalidHeartbeat) {
		t.Fatalf("HandleHeartbeatSession() error = %v, want ErrInvalidHeartbeat", err)
	}
	if len(session.writes) != 0 {
		t.Fatalf("written frames = %v, want none", session.writes)
	}
}

func TestHandleHeartbeatSessionWrapsReadAndWriteErrors(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		readErr := errors.New("read failed")
		err := HandleHeartbeatSession(context.Background(), &heartbeatTestSession{readErr: readErr})
		if !errors.Is(err, readErr) {
			t.Fatalf("HandleHeartbeatSession() error = %v, want wrapped read error", err)
		}
	})

	t.Run("write", func(t *testing.T) {
		writeErr := errors.New("write failed")
		session := &heartbeatTestSession{
			frames:   []protocol.Frame{{Type: protocol.TypePing}},
			writeErr: writeErr,
		}
		err := HandleHeartbeatSession(context.Background(), session)
		if !errors.Is(err, writeErr) {
			t.Fatalf("HandleHeartbeatSession() error = %v, want wrapped write error", err)
		}
	})
}

func TestHandleHeartbeatSessionTreatsCancellationAsCleanExit(t *testing.T) {
	t.Run("read failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := HandleHeartbeatSession(ctx, &heartbeatTestSession{readErr: errors.New("closed")})
		if err != nil {
			t.Fatalf("HandleHeartbeatSession() error = %v, want nil", err)
		}
	})

	t.Run("write failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		session := &heartbeatTestSession{
			frames:   []protocol.Frame{{Type: protocol.TypePing}},
			writeErr: errors.New("closed"),
		}
		if err := HandleHeartbeatSession(ctx, session); err != nil {
			t.Fatalf("HandleHeartbeatSession() error = %v, want nil", err)
		}
	})
}

func TestHeartbeatHandlerOverAuthenticatedSession(t *testing.T) {
	serverIdentity := serverTestIdentity(t)
	clientIdentity := serverTestIdentity(t)
	listener := serverTestListener(t)
	errorsReported := make(chan error, 1)
	server := serverTestNew(
		t,
		listener,
		serverIdentity,
		serverTestLookup(clientIdentity),
		NewRegistry(),
		HandleHeartbeatSession,
		func(err error) { errorsReported <- err },
	)
	cancel, result := serverTestStart(t, server)

	client := serverTestDial(t, listener.Addr().String(), serverIdentity, clientIdentity)
	defer client.Close()
	if err := client.WriteFrame(protocol.TypePing, nil); err != nil {
		t.Fatalf("WriteFrame(PING) error = %v", err)
	}
	frame, err := client.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(PONG) error = %v", err)
	}
	if frame.Type != protocol.TypePong || len(frame.Payload) != 0 {
		t.Fatalf("heartbeat response = {%s %q}, want {PONG empty}", frame.Type, frame.Payload)
	}

	serverTestStop(t, cancel, result)
	select {
	case err := <-errorsReported:
		t.Fatalf("unexpected reported server error: %v", err)
	default:
	}
}

type heartbeatTestSession struct {
	frames   []protocol.Frame
	readErr  error
	writes   []protocol.Frame
	writeErr error
}

func (s *heartbeatTestSession) PeerNodeID() string { return "heartbeat-test-peer" }

func (s *heartbeatTestSession) WriteFrame(messageType protocol.MessageType, payload []byte) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, protocol.Frame{Type: messageType, Payload: payload})
	return nil
}

func (s *heartbeatTestSession) ReadFrame() (protocol.Frame, error) {
	if len(s.frames) == 0 {
		return protocol.Frame{}, s.readErr
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}

func (s *heartbeatTestSession) LocalAddr() net.Addr  { return nil }
func (s *heartbeatTestSession) RemoteAddr() net.Addr { return nil }
func (s *heartbeatTestSession) Close() error         { return nil }
