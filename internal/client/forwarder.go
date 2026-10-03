package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Haruko386/GoBridge/internal/proxy"
)

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type ProxyForwarder struct {
	address string
	dial    dialContextFunc
}

func NewProxyForwarder(address string, dialTimeout time.Duration) (*ProxyForwarder, error) {
	if dialTimeout <= 0 {
		return nil, errors.New("proxy dial timeout must be positive")
	}

	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return newProxyForwarder(address, dialer.DialContext)
}

func newProxyForwarder(address string, dial dialContextFunc) (*ProxyForwarder, error) {
	if address == "" {
		return nil, errors.New("proxy address is empty")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, fmt.Errorf("invalid proxy address %q: %w", address, err)
	}
	if dial == nil {
		return nil, errors.New("proxy dial function is nil")
	}

	return &ProxyForwarder{address: address, dial: dial}, nil
}

func (f *ProxyForwarder) Forward(ctx context.Context, stream proxy.Endpoint) error {
	if ctx == nil {
		return errors.New("proxy forward context is nil")
	}
	if stream == nil {
		return errors.New("proxy forward stream is nil")
	}

	defer stream.Close()

	connection, err := f.dial(ctx, "tcp", f.address)
	if err != nil {
		return fmt.Errorf("dial proxy %s: %w", f.address, err)
	}
	defer connection.Close()

	if err := proxy.Bridge(ctx, stream, connection); err != nil {
		return fmt.Errorf("bridge stream to proxy %s: %w", f.address, err)
	}
	return nil
}

func (f *ProxyForwarder) Address() string {
	return f.address
}
