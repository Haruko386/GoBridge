package server

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/proxy"
)

func TestNewProxyServerValidatesArguments(t *testing.T) {
	listener := proxyTestListener(t)
	defer listener.Close()

	if _, err := NewProxyServer(nil, NewTunnelRegistry(), nil); err == nil {
		t.Fatal("NewProxyServer(nil listener) error = nil")
	}
	if _, err := NewProxyServer(listener, nil, nil); err == nil {
		t.Fatal("NewProxyServer(nil registry) error = nil")
	}
}

func TestProxyServerForwardsConnection(t *testing.T) {
	listener := proxyTestListener(t)
	registry := NewTunnelRegistry()
	_, err := registry.Register("client-node", func(context.Context) (proxy.Endpoint, error) {
		stream, peer := net.Pipe()
		go func() {
			defer peer.Close()
			request := make([]byte, len("request"))
			if _, err := io.ReadFull(peer, request); err != nil {
				return
			}
			_, _ = peer.Write([]byte("response:" + string(request)))
		}()
		return stream, nil
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	server, err := NewProxyServer(listener, registry, nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(ctx)
	}()

	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if _, err := connection.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := make([]byte, len("response:request"))
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(response) != "response:request" {
		t.Fatalf("response = %q, want %q", response, "response:request")
	}
	_ = connection.Close()

	cancel()
	if err := proxyTestResult(t, result); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestProxyServerReportsOfflineProvider(t *testing.T) {
	listener := proxyTestListener(t)
	errorsReported := make(chan error, 1)
	server, err := NewProxyServer(listener, NewTunnelRegistry(), func(err error) {
		errorsReported <- err
	})
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(ctx)
	}()

	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer connection.Close()

	select {
	case err := <-errorsReported:
		if !errors.Is(err, ErrProxyProviderOffline) {
			t.Fatalf("reported error = %v, want ErrProxyProviderOffline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("offline provider error was not reported")
	}

	cancel()
	if err := proxyTestResult(t, result); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestProxyServerRejectsNilContext(t *testing.T) {
	listener := proxyTestListener(t)
	defer listener.Close()
	server, err := NewProxyServer(listener, NewTunnelRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	if err := server.Serve(nil); err == nil {
		t.Fatal("Serve(nil) error = nil")
	}
}

func proxyTestListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	return listener
}

func proxyTestResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("proxy server did not return")
		return nil
	}
}
