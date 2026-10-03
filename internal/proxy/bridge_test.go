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

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Bridge() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Bridge() did not return after an endpoint closed")
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

type failingReadEndpoint struct {
	err    error
	closed atomic.Bool
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
