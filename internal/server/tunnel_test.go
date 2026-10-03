package server

import (
	"testing"
	"time"
)

func TestNewTunnelHandlerValidatesConfiguration(t *testing.T) {
	if _, err := NewTunnelHandler(nil, time.Second); err == nil {
		t.Fatal("NewTunnelHandler(nil registry) error = nil")
	}
	if _, err := NewTunnelHandler(NewTunnelRegistry(), 0); err == nil {
		t.Fatal("NewTunnelHandler(zero timeout) error = nil")
	}
	if _, err := NewTunnelHandler(NewTunnelRegistry(), time.Second); err != nil {
		t.Fatalf("NewTunnelHandler() error = %v", err)
	}
}
