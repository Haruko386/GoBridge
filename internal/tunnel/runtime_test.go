package tunnel

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/protocol"
)

func TestNewRuntimeValidatesArguments(t *testing.T) {
	session := newRuntimeTestSession()
	manager := mustNewManager(t, session, FirstServerStreamID, nil)

	if _, err := NewRuntime(nil, manager, 0, time.Second); err == nil {
		t.Fatal("NewRuntime(nil session) error = nil")
	}
	if _, err := NewRuntime(session, nil, 0, time.Second); err == nil {
		t.Fatal("NewRuntime(nil manager) error = nil")
	}
	if _, err := NewRuntime(session, manager, -1, time.Second); err == nil {
		t.Fatal("NewRuntime(negative interval) error = nil")
	}
	if _, err := NewRuntime(session, manager, 0, 0); err == nil {
		t.Fatal("NewRuntime(zero timeout) error = nil")
	}
}

func TestRuntimeRoutesHeartbeatAndTunnelFrames(t *testing.T) {
	session := newRuntimeTestSession()
	manager := mustNewManager(t, session, FirstServerStreamID, nil)
	runtime := mustNewRuntime(t, session, manager, 0, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runtime.Run(ctx)
	}()

	session.reads <- protocol.Frame{Type: protocol.TypePing}
	pong := runtimeTestFrame(t, session.writes)
	if pong.Type != protocol.TypePong || len(pong.Payload) != 0 {
		t.Fatalf("heartbeat response = %#v, want empty PONG", pong)
	}

	openResult := startOpen(manager)
	request := runtimeTestFrame(t, session.writes)
	if request.Type != protocol.TypeOpenStream {
		t.Fatalf("frame type = %s, want OPEN_STREAM", request.Type)
	}
	id, err := protocol.DecodeStreamID(request.Payload)
	if err != nil {
		t.Fatalf("DecodeStreamID() error = %v", err)
	}
	openOK, err := protocol.EncodeStreamID(id)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	session.reads <- protocol.Frame{Type: protocol.TypeOpenOK, Payload: openOK}
	if stream := requireOpenSuccess(t, openResult); stream.ID() != id {
		t.Fatalf("opened stream ID = %d, want %d", stream.ID(), id)
	}

	cancel()
	if err := runtimeTestResult(t, result); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if session.maxReaders.Load() != 1 {
		t.Fatalf("maximum concurrent readers = %d, want 1", session.maxReaders.Load())
	}
}

func TestRuntimeSendsHeartbeats(t *testing.T) {
	session := newRuntimeTestSession()
	manager := mustNewManager(t, session, FirstClientStreamID, func(*Stream) {})
	runtime := mustNewRuntime(t, session, manager, time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runtime.Run(ctx)
	}()

	for range 2 {
		ping := runtimeTestFrame(t, session.writes)
		if ping.Type != protocol.TypePing || len(ping.Payload) != 0 {
			t.Fatalf("heartbeat request = %#v, want empty PING", ping)
		}
		session.reads <- protocol.Frame{Type: protocol.TypePong}
	}

	cancel()
	if err := runtimeTestResult(t, result); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRuntimeHeartbeatTimeoutClosesSession(t *testing.T) {
	session := newRuntimeTestSession()
	manager := mustNewManager(t, session, FirstClientStreamID, func(*Stream) {})
	runtime := mustNewRuntime(t, session, manager, time.Second, 10*time.Millisecond)

	err := runtime.Run(context.Background())
	if !errors.Is(err, ErrHeartbeatTimeout) {
		t.Fatalf("Run() error = %v, want ErrHeartbeatTimeout", err)
	}
	if session.closeCalls.Load() != 1 {
		t.Fatalf("session close count = %d, want 1", session.closeCalls.Load())
	}
}

func TestRuntimeRejectsInvalidAndUnexpectedFrames(t *testing.T) {
	tests := []struct {
		name  string
		frame protocol.Frame
		want  error
	}{
		{name: "PING payload", frame: protocol.Frame{Type: protocol.TypePing, Payload: []byte("invalid")}, want: ErrInvalidHeartbeat},
		{name: "unsolicited PONG", frame: protocol.Frame{Type: protocol.TypePong}, want: ErrUnexpectedSessionFrame},
		{name: "authentication frame", frame: protocol.Frame{Type: protocol.TypeAuthOK}, want: ErrUnexpectedSessionFrame},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newRuntimeTestSession()
			manager := mustNewManager(t, session, FirstServerStreamID, nil)
			runtime := mustNewRuntime(t, session, manager, 0, time.Second)
			session.reads <- test.frame

			if err := runtime.Run(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRuntimeReportsReadFailure(t *testing.T) {
	wantErr := errors.New("read failed")
	session := newRuntimeTestSession()
	session.readErrs <- wantErr
	manager := mustNewManager(t, session, FirstServerStreamID, nil)
	runtime := mustNewRuntime(t, session, manager, 0, time.Second)

	if err := runtime.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want read error", err)
	}
}

type runtimeTestSession struct {
	reads      chan protocol.Frame
	readErrs   chan error
	writes     chan protocol.Frame
	done       chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
	readers    atomic.Int32
	maxReaders atomic.Int32
}

func newRuntimeTestSession() *runtimeTestSession {
	return &runtimeTestSession{
		reads:    make(chan protocol.Frame, 16),
		readErrs: make(chan error, 1),
		writes:   make(chan protocol.Frame, 16),
		done:     make(chan struct{}),
	}
}

func (s *runtimeTestSession) WriteFrame(messageType protocol.MessageType, payload []byte) error {
	select {
	case s.writes <- protocol.Frame{Type: messageType, Payload: bytes.Clone(payload)}:
		return nil
	case <-s.done:
		return net.ErrClosed
	}
}

func (s *runtimeTestSession) ReadFrame() (protocol.Frame, error) {
	readers := s.readers.Add(1)
	defer s.readers.Add(-1)
	for {
		maximum := s.maxReaders.Load()
		if readers <= maximum || s.maxReaders.CompareAndSwap(maximum, readers) {
			break
		}
	}

	select {
	case frame := <-s.reads:
		return frame, nil
	case err := <-s.readErrs:
		return protocol.Frame{}, err
	case <-s.done:
		return protocol.Frame{}, net.ErrClosed
	}
}

func (s *runtimeTestSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCalls.Add(1)
		close(s.done)
	})
	return nil
}

func mustNewRuntime(t *testing.T, session Session, manager *Manager, interval, timeout time.Duration) *Runtime {
	t.Helper()

	runtime, err := NewRuntime(session, manager, interval, timeout)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func runtimeTestFrame(t *testing.T, frames <-chan protocol.Frame) protocol.Frame {
	t.Helper()

	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("frame was not written")
		return protocol.Frame{}
	}
}

func runtimeTestResult(t *testing.T, results <-chan error) error {
	t.Helper()

	select {
	case err := <-results:
		return err
	case <-time.After(time.Second):
		t.Fatal("runtime did not return")
		return nil
	}
}
