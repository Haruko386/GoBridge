package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBridgeValidatesArguments(t *testing.T) {
	leftBridge, leftPeer := net.Pipe()
	defer leftBridge.Close()
	defer leftPeer.Close()

	if err := Bridge(nil, leftBridge, leftPeer); err == nil {
		t.Fatal("Bridge(nil context) error = nil, want an error")
	}
	if err := Bridge(context.Background(), nil, leftPeer); err == nil {
		t.Fatal("Bridge(nil left) error = nil, want an error")
	}
	if err := Bridge(context.Background(), leftBridge, nil); err == nil {
		t.Fatal("Bridge(nil right) error = nil, want an error")
	}
}

func TestBridgeCopiesDataInBothDirections(t *testing.T) {
	leftBridge, leftPeer := net.Pipe()
	rightBridge, rightPeer := net.Pipe()
	defer leftPeer.Close()
	defer rightPeer.Close()

	result := make(chan error, 1)
	go func() {
		result <- Bridge(context.Background(), leftBridge, rightBridge)
	}()

	assertBridgeTransfer(t, leftPeer, rightPeer, []byte("request from left"))
	assertBridgeTransfer(t, rightPeer, leftPeer, []byte("response from right"))

	if err := leftPeer.Close(); err != nil {
		t.Fatalf("close left peer: %v", err)
	}
	if err := rightPeer.Close(); err != nil {
		t.Fatalf("close right peer: %v", err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Bridge() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Bridge() did not return after both endpoints closed")
	}
}

func TestBridgeKeepsReverseDirectionOpenAfterEOF(t *testing.T) {
	leftBridge, leftPeer := newTCPPair(t)
	rightBridge, rightPeer := newTCPPair(t)
	defer leftPeer.Close()
	defer rightPeer.Close()

	result := make(chan error, 1)
	go func() {
		result <- Bridge(context.Background(), leftBridge, rightBridge)
	}()

	request := []byte("request")
	if _, err := leftPeer.Write(request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := leftPeer.CloseWrite(); err != nil {
		t.Fatalf("half-close request direction: %v", err)
	}

	if err := rightPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set right read deadline: %v", err)
	}
	gotRequest := make([]byte, len(request))
	if _, err := io.ReadFull(rightPeer, gotRequest); err != nil {
		t.Fatalf("read request: %v", err)
	}
	if string(gotRequest) != string(request) {
		t.Fatalf("request = %q, want %q", gotRequest, request)
	}

	response := []byte("response")
	if _, err := rightPeer.Write(response); err != nil {
		t.Fatalf("write response: %v", err)
	}
	if err := rightPeer.CloseWrite(); err != nil {
		t.Fatalf("half-close response direction: %v", err)
	}

	if err := leftPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set left read deadline: %v", err)
	}
	gotResponse := make([]byte, len(response))
	if _, err := io.ReadFull(leftPeer, gotResponse); err != nil {
		t.Fatalf("read response after request EOF: %v", err)
	}
	if string(gotResponse) != string(response) {
		t.Fatalf("response = %q, want %q", gotResponse, response)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Bridge() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Bridge() did not return after both directions ended")
	}
}

func TestBridgeCancellationClosesEndpoints(t *testing.T) {
	leftBridge, leftPeer := net.Pipe()
	rightBridge, rightPeer := net.Pipe()
	defer leftPeer.Close()
	defer rightPeer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- Bridge(ctx, leftBridge, rightBridge)
	}()

	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Bridge() error after cancellation = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Bridge() did not return after cancellation")
	}

	if _, err := leftPeer.Write([]byte("closed")); err == nil {
		t.Fatal("left peer write succeeded after Bridge cancellation")
	}
	if _, err := rightPeer.Write([]byte("closed")); err == nil {
		t.Fatal("right peer write succeeded after Bridge cancellation")
	}
}

func TestBridgeReportsTheFailingDirection(t *testing.T) {
	wantErr := errors.New("left read failed")
	left := &failingReadEndpoint{err: wantErr}
	right := newBlockingEndpoint()

	err := Bridge(context.Background(), left, right)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Bridge() error = %v, want %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "copy left to right") {
		t.Fatalf("Bridge() error = %q, want left-to-right direction", err)
	}
	if !left.closed.Load() {
		t.Fatal("left endpoint was not closed")
	}
	if !right.closed.Load() {
		t.Fatal("right endpoint was not closed")
	}
}

func TestBridgeReturnsEndpointCloseErrors(t *testing.T) {
	leftErr := errors.New("close left failed")
	rightErr := errors.New("close right failed")

	err := Bridge(
		context.Background(),
		&eofCloseEndpoint{closeErr: leftErr},
		&eofCloseEndpoint{closeErr: rightErr},
	)
	if !errors.Is(err, leftErr) {
		t.Fatalf("Bridge() error = %v, want left close error", err)
	}
	if !errors.Is(err, rightErr) {
		t.Fatalf("Bridge() error = %v, want right close error", err)
	}
}

func TestNormalizeCopyError(t *testing.T) {
	for _, err := range []error{
		nil,
		io.EOF,
		io.ErrClosedPipe,
		net.ErrClosed,
	} {
		if got := normalizeCopyError(copyResult{direction: "test", err: err}); got != nil {
			t.Fatalf("normalizeCopyError(%v) = %v, want nil", err, got)
		}
	}

	wantErr := errors.New("copy failed")
	got := normalizeCopyError(copyResult{
		direction: "left to right",
		err:       wantErr,
	})
	if !errors.Is(got, wantErr) {
		t.Fatalf("normalizeCopyError() = %v, want wrapped error", got)
	}
}

func assertBridgeTransfer(
	t *testing.T,
	source net.Conn,
	destination net.Conn,
	data []byte,
) {
	t.Helper()

	writeResult := make(chan error, 1)
	go func() {
		_, err := source.Write(data)
		writeResult <- err
	}()

	got := make([]byte, len(data))
	if _, err := io.ReadFull(destination, got); err != nil {
		t.Fatalf("read bridged data: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("bridged data = %q, want %q", got, data)
	}

	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("write source data: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source write did not complete")
	}
}

func newTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenTCP() error = %v", err)
	}
	defer listener.Close()

	accepted := make(chan *net.TCPConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := listener.AcceptTCP()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()

	peer, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatalf("DialTCP() error = %v", err)
	}

	select {
	case bridge := <-accepted:
		return bridge, peer
	case err := <-acceptErr:
		_ = peer.Close()
		t.Fatalf("AcceptTCP() error = %v", err)
	case <-time.After(time.Second):
		_ = peer.Close()
		t.Fatal("AcceptTCP() did not return")
	}

	return nil, nil
}

type failingReadEndpoint struct {
	err    error
	closed atomic.Bool
}

type eofCloseEndpoint struct {
	closeErr error
}

func (e *eofCloseEndpoint) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (e *eofCloseEndpoint) Write(data []byte) (int, error) {
	return len(data), nil
}

func (e *eofCloseEndpoint) Close() error {
	return e.closeErr
}

func (e *failingReadEndpoint) Read([]byte) (int, error) {
	return 0, e.err
}

func (e *failingReadEndpoint) Write(data []byte) (int, error) {
	return len(data), nil
}

func (e *failingReadEndpoint) Close() error {
	e.closed.Store(true)
	return nil
}

type blockingEndpoint struct {
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
}

func newBlockingEndpoint() *blockingEndpoint {
	return &blockingEndpoint{done: make(chan struct{})}
}

func (e *blockingEndpoint) Read([]byte) (int, error) {
	<-e.done
	return 0, net.ErrClosed
}

func (e *blockingEndpoint) Write(data []byte) (int, error) {
	return len(data), nil
}

func (e *blockingEndpoint) Close() error {
	e.closeOnce.Do(func() {
		e.closed.Store(true)
		close(e.done)
	})
	return nil
}
