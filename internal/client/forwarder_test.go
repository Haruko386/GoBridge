package client

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewProxyForwarderValidatesConfiguration(t *testing.T) {
	if _, err := NewProxyForwarder("127.0.0.1:7897", 0); err == nil {
		t.Fatal("NewProxyForwarder(zero timeout) error = nil")
	}
	if _, err := newProxyForwarder("", func(context.Context, string, string) (net.Conn, error) { return nil, nil }); err == nil {
		t.Fatal("newProxyForwarder(empty address) error = nil")
	}
	if _, err := newProxyForwarder("invalid", func(context.Context, string, string) (net.Conn, error) { return nil, nil }); err == nil {
		t.Fatal("newProxyForwarder(invalid address) error = nil")
	}
	if _, err := newProxyForwarder("127.0.0.1:7897", nil); err == nil {
		t.Fatal("newProxyForwarder(nil dial) error = nil")
	}
}

func TestProxyForwarderTransfersData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	upstreamResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			upstreamResult <- err
			return
		}
		defer connection.Close()

		request := make([]byte, len("request"))
		if _, err := io.ReadFull(connection, request); err != nil {
			upstreamResult <- err
			return
		}
		_, err = connection.Write([]byte("response:" + string(request)))
		upstreamResult <- err
	}()

	forwarder, err := NewProxyForwarder(listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("NewProxyForwarder() error = %v", err)
	}
	if forwarder.Address() != listener.Addr().String() {
		t.Fatalf("Address() = %q, want %q", forwarder.Address(), listener.Addr().String())
	}

	stream, peer := net.Pipe()
	forwardResult := make(chan error, 1)
	go func() {
		forwardResult <- forwarder.Forward(context.Background(), stream)
	}()

	if _, err := peer.Write([]byte("request")); err != nil {
		t.Fatalf("write stream request: %v", err)
	}
	response := make([]byte, len("response:request"))
	if _, err := io.ReadFull(peer, response); err != nil {
		t.Fatalf("read stream response: %v", err)
	}
	if string(response) != "response:request" {
		t.Fatalf("response = %q, want %q", response, "response:request")
	}
	if err := peer.Close(); err != nil {
		t.Fatalf("close stream peer: %v", err)
	}

	if err := receiveForwarderResult(t, upstreamResult, "upstream"); err != nil {
		t.Fatalf("upstream error = %v", err)
	}
	if err := receiveForwarderResult(t, forwardResult, "forwarder"); err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
}

func TestProxyForwarderClosesStreamAfterDialFailure(t *testing.T) {
	wantErr := errors.New("dial failed")
	forwarder, err := newProxyForwarder("127.0.0.1:7897", func(context.Context, string, string) (net.Conn, error) {
		return nil, wantErr
	})
	if err != nil {
		t.Fatalf("newProxyForwarder() error = %v", err)
	}

	stream := newForwarderTestEndpoint()
	err = forwarder.Forward(context.Background(), stream)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Forward() error = %v, want dial error", err)
	}
	if stream.closeCalls.Load() != 1 {
		t.Fatalf("stream close count = %d, want 1", stream.closeCalls.Load())
	}
}

func TestProxyForwarderValidatesArguments(t *testing.T) {
	forwarder, err := NewProxyForwarder("127.0.0.1:7897", time.Second)
	if err != nil {
		t.Fatalf("NewProxyForwarder() error = %v", err)
	}

	if err := forwarder.Forward(nil, newForwarderTestEndpoint()); err == nil {
		t.Fatal("Forward(nil context) error = nil")
	}
	if err := forwarder.Forward(context.Background(), nil); err == nil {
		t.Fatal("Forward(nil stream) error = nil")
	}
}

type forwarderTestEndpoint struct {
	done       chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newForwarderTestEndpoint() *forwarderTestEndpoint {
	return &forwarderTestEndpoint{done: make(chan struct{})}
}

func (e *forwarderTestEndpoint) Read([]byte) (int, error) {
	<-e.done
	return 0, net.ErrClosed
}

func (e *forwarderTestEndpoint) Write(data []byte) (int, error) {
	return len(data), nil
}

func (e *forwarderTestEndpoint) Close() error {
	e.closeOnce.Do(func() {
		e.closeCalls.Add(1)
		close(e.done)
	})
	return nil
}

func receiveForwarderResult(t *testing.T, result <-chan error, name string) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatalf("%s did not return", name)
		return nil
	}
}
