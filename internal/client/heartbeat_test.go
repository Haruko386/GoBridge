package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/protocol"
	"github.com/Haruko386/GoBridge/internal/server"
	"github.com/Haruko386/GoBridge/internal/transport"
)

const clientHeartbeatTestTimeout = 2 * time.Second

func TestRunHeartbeatRunsMultipleRoundsAndStopsCleanly(t *testing.T) {
	session := newClientHeartbeatTestSession()
	session.onWrite = func(frame protocol.Frame) {
		session.reads <- clientHeartbeatRead{frame: protocol.Frame{Type: protocol.TypePong}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunHeartbeat(ctx, session, time.Millisecond, time.Second)
	}()

	for round := 0; round < 3; round++ {
		frame := clientHeartbeatReceive(t, session.writes, "PING")
		if frame.Type != protocol.TypePing || len(frame.Payload) != 0 {
			t.Fatalf("written frame = {%s %q}, want {PING empty}", frame.Type, frame.Payload)
		}
	}
	cancel()
	if err := clientHeartbeatReceive(t, result, "heartbeat shutdown"); err != nil {
		t.Fatalf("RunHeartbeat() error = %v, want nil", err)
	}
	if got := session.closeCount.Load(); got != 1 {
		t.Fatalf("Close() count = %d, want 1", got)
	}
	if got := session.maxReaders.Load(); got != 1 {
		t.Fatalf("maximum concurrent readers = %d, want 1", got)
	}
}

func TestRunHeartbeatValidatesArguments(t *testing.T) {
	valid := newClientHeartbeatTestSession()
	if err := RunHeartbeat(nil, valid, time.Second, time.Second); err == nil {
		t.Fatal("RunHeartbeat(nil context) error = nil")
	}
	if err := RunHeartbeat(context.Background(), nil, time.Second, time.Second); err == nil {
		t.Fatal("RunHeartbeat(nil session) error = nil")
	}
	if err := RunHeartbeat(context.Background(), valid, 0, time.Second); err == nil {
		t.Fatal("RunHeartbeat(zero interval) error = nil")
	}
	if err := RunHeartbeat(context.Background(), valid, time.Second, 0); err == nil {
		t.Fatal("RunHeartbeat(zero timeout) error = nil")
	}
}

func TestRunHeartbeatClosesAlreadyCanceledSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := newClientHeartbeatTestSession()

	if err := RunHeartbeat(ctx, session, time.Second, time.Second); err != nil {
		t.Fatalf("RunHeartbeat() error = %v, want nil", err)
	}
	if got := session.closeCount.Load(); got != 1 {
		t.Fatalf("Close() count = %d, want 1", got)
	}
}

func TestRunHeartbeatTimesOutAndClosesSession(t *testing.T) {
	session := newClientHeartbeatTestSession()
	err := RunHeartbeat(context.Background(), session, time.Second, 10*time.Millisecond)
	if !errors.Is(err, ErrHeartbeatTimeout) {
		t.Fatalf("RunHeartbeat() error = %v, want ErrHeartbeatTimeout", err)
	}
	if got := session.closeCount.Load(); got != 1 {
		t.Fatalf("Close() count = %d, want 1", got)
	}
}

func TestRunHeartbeatReportsReadAndWriteFailures(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		writeErr := errors.New("write failed")
		session := newClientHeartbeatTestSession()
		session.writeErr = writeErr
		if err := RunHeartbeat(context.Background(), session, time.Second, time.Second); !errors.Is(err, writeErr) {
			t.Fatalf("RunHeartbeat() error = %v, want wrapped write error", err)
		}
	})

	t.Run("read", func(t *testing.T) {
		readErr := errors.New("read failed")
		session := newClientHeartbeatTestSession()
		session.onWrite = func(protocol.Frame) {
			session.reads <- clientHeartbeatRead{err: readErr}
		}
		if err := RunHeartbeat(context.Background(), session, time.Second, time.Second); !errors.Is(err, readErr) {
			t.Fatalf("RunHeartbeat() error = %v, want wrapped read error", err)
		}
	})
}

func TestRunHeartbeatValidatesResponses(t *testing.T) {
	tests := []struct {
		name     string
		response protocol.Frame
		wantErr  error
	}{
		{name: "unexpected type", response: protocol.Frame{Type: protocol.TypePing}, wantErr: ErrUnexpectedHeartbeatFrame},
		{name: "non-empty payload", response: protocol.Frame{Type: protocol.TypePong, Payload: []byte("data")}, wantErr: ErrInvalidHeartbeat},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newClientHeartbeatTestSession()
			session.onWrite = func(protocol.Frame) {
				session.reads <- clientHeartbeatRead{frame: test.response}
			}
			err := RunHeartbeat(context.Background(), session, time.Second, time.Second)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RunHeartbeat() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRunHeartbeatRejectsUnsolicitedFrameBetweenRounds(t *testing.T) {
	session := newClientHeartbeatTestSession()
	session.onWrite = func(protocol.Frame) {
		session.reads <- clientHeartbeatRead{frame: protocol.Frame{Type: protocol.TypePong}}
		session.reads <- clientHeartbeatRead{frame: protocol.Frame{Type: protocol.TypePong}}
	}

	err := RunHeartbeat(context.Background(), session, time.Second, time.Second)
	if !errors.Is(err, ErrUnexpectedHeartbeatFrame) {
		t.Fatalf("RunHeartbeat() error = %v, want ErrUnexpectedHeartbeatFrame", err)
	}
}

func TestRunHeartbeatWithRealServer(t *testing.T) {
	serverIdentity := clientHeartbeatIdentity(t)
	clientIdentity := clientHeartbeatIdentity(t)
	listener := clientHeartbeatListener(t)
	registry := server.NewRegistry()
	serverInstance, err := server.NewServer(
		listener,
		serverIdentity,
		clientHeartbeatLookup(clientIdentity),
		registry,
		clientHeartbeatTestTimeout,
		server.HandleHeartbeatSession,
		func(error) {},
	)
	if err != nil {
		t.Fatalf("server.NewServer() error = %v", err)
	}

	serverCtx, stopServer := context.WithCancel(context.Background())
	serverResult := make(chan error, 1)
	go func() { serverResult <- serverInstance.Serve(serverCtx) }()

	tlsConfig, err := transport.NewClientTLSConfig(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("transport.NewClientTLSConfig() error = %v", err)
	}
	session, err := transport.DialClientSession(context.Background(), listener.Addr().String(), tlsConfig, clientIdentity, clientHeartbeatTestTimeout)
	if err != nil {
		t.Fatalf("transport.DialClientSession() error = %v", err)
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	heartbeatResult := make(chan error, 1)
	go func() {
		heartbeatResult <- RunHeartbeat(heartbeatCtx, session, time.Millisecond, time.Second)
	}()
	clientHeartbeatEventually(t, func() bool { return registry.Len() == 1 }, "client registration")
	time.Sleep(10 * time.Millisecond)
	stopHeartbeat()
	if err := clientHeartbeatReceive(t, heartbeatResult, "real heartbeat shutdown"); err != nil {
		t.Fatalf("RunHeartbeat() error = %v, want nil", err)
	}

	stopServer()
	if err := clientHeartbeatReceive(t, serverResult, "server shutdown"); err != nil {
		t.Fatalf("Server.Serve() error = %v", err)
	}
}

type clientHeartbeatRead struct {
	frame protocol.Frame
	err   error
}

type clientHeartbeatTestSession struct {
	reads  chan clientHeartbeatRead
	writes chan protocol.Frame
	closed chan struct{}

	onWrite  func(protocol.Frame)
	writeErr error

	closeOnce  sync.Once
	closeCount atomic.Int32
	readers    atomic.Int32
	maxReaders atomic.Int32
}

func newClientHeartbeatTestSession() *clientHeartbeatTestSession {
	return &clientHeartbeatTestSession{
		reads:  make(chan clientHeartbeatRead, 16),
		writes: make(chan protocol.Frame, 16),
		closed: make(chan struct{}),
	}
}

func (s *clientHeartbeatTestSession) WriteFrame(messageType protocol.MessageType, payload []byte) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	frame := protocol.Frame{Type: messageType, Payload: payload}
	s.writes <- frame
	if s.onWrite != nil {
		s.onWrite(frame)
	}
	return nil
}

func (s *clientHeartbeatTestSession) ReadFrame() (protocol.Frame, error) {
	readers := s.readers.Add(1)
	defer s.readers.Add(-1)
	for {
		maximum := s.maxReaders.Load()
		if readers <= maximum || s.maxReaders.CompareAndSwap(maximum, readers) {
			break
		}
	}
	select {
	case result := <-s.reads:
		return result.frame, result.err
	case <-s.closed:
		return protocol.Frame{}, net.ErrClosed
	}
}

func (s *clientHeartbeatTestSession) Close() error {
	s.closeCount.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func clientHeartbeatIdentity(t *testing.T) identity.Identity {
	t.Helper()
	value, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	return value
}

func clientHeartbeatLookup(nodeIdentity identity.Identity) transport.PublicKeyLookup {
	return func(nodeID string) (ed25519.PublicKey, bool) {
		if nodeID != nodeIdentity.NodeID() {
			return nil, false
		}
		return nodeIdentity.PublicKey(), true
	}
}

func clientHeartbeatListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	return listener
}

func clientHeartbeatReceive[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(clientHeartbeatTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func clientHeartbeatEventually(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(clientHeartbeatTestTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
