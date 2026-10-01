package cli

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/protocol"
	"github.com/Haruko386/GoBridge/internal/transport"
)

const serveCLITestTimeout = 3 * time.Second

func TestRunContextRejectsNilContext(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if got := RunContext(nil, nil, &stdout, &stderr, "test-version"); got != 1 {
		t.Fatalf("RunContext(nil) exit code = %d, want 1", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "CLI context is nil") {
		t.Fatalf("stderr = %q, want nil-context error", stderr.String())
	}
}

func TestRunServeUsageErrors(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{name: "help", args: []string{"serve", "--help"}, wantExit: 0, wantStderr: "Usage: gobridge serve"},
		{name: "unknown flag", args: []string{"serve", "--unknown"}, wantExit: 2, wantStderr: "flag provided but not defined"},
		{name: "unexpected argument", args: []string{"serve", "extra"}, wantExit: 2, wantStderr: "unexpected arguments"},
		{name: "zero auth timeout", args: []string{"serve", "--auth-timeout", "0s"}, wantExit: 2, wantStderr: "must be positive"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			got := RunContext(context.Background(), test.args, &stdout, &stderr, "test-version")
			if got != test.wantExit {
				t.Fatalf("RunContext() exit code = %d, want %d", got, test.wantExit)
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestRunServeRejectsClientConfiguration(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleClient, "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := RunContext(
		context.Background(),
		[]string{"serve", "--config-dir", dir},
		&stdout,
		&stderr,
		"test-version",
	)
	if exitCode != 1 {
		t.Fatalf("RunContext(serve client) exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), `configured role is "client"`) {
		t.Fatalf("stderr = %q, want client-role error", stderr.String())
	}
}

func TestRunServeReportsListenFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer occupied.Close()
	dir := serveCLITestNode(t, config.RoleServer, occupied.Addr().String())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := RunContext(
		context.Background(),
		[]string{"serve", "--config-dir", dir},
		&stdout,
		&stderr,
		"test-version",
	)
	if exitCode != 1 {
		t.Fatalf("RunContext(occupied listener) exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "serve: listen on") {
		t.Fatalf("stderr = %q, want listen error", stderr.String())
	}
}

func TestRunServeAuthenticatesAndHandlesHeartbeat(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleServer, "127.0.0.1:0")
	serverIdentity, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("identity.Load(server) error = %v", err)
	}
	clientIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate(client) error = %v", err)
	}
	store, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open() error = %v", err)
	}
	clientPeer, err := peer.New("test-client", clientIdentity.PublicKey())
	if err != nil {
		t.Fatalf("peer.New() error = %v", err)
	}
	if err := store.Add(clientPeer); err != nil {
		t.Fatalf("Store.Add() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	result := make(chan int, 1)
	go func() {
		result <- RunContext(
			ctx,
			[]string{"serve", "--config-dir", dir, "--auth-timeout", "1s"},
			&stdout,
			&stderr,
			"test-version",
		)
	}()
	defer func() {
		cancel()
		select {
		case <-result:
		default:
		}
	}()

	serveCLIEventually(t, func() bool {
		return strings.Contains(stdout.String(), "GoBridge server listening on ")
	}, "serve startup output")
	address := serveCLIListeningAddress(t, stdout.String())

	tlsConfig, err := transport.NewClientTLSConfig(serverIdentity.PublicKey())
	if err != nil {
		t.Fatalf("transport.NewClientTLSConfig() error = %v", err)
	}
	clientSession, err := transport.DialClientSession(
		context.Background(),
		address,
		tlsConfig,
		clientIdentity,
		time.Second,
	)
	if err != nil {
		t.Fatalf("transport.DialClientSession() error = %v", err)
	}
	defer clientSession.Close()

	if err := clientSession.WriteFrame(protocol.TypePing, nil); err != nil {
		t.Fatalf("WriteFrame(PING) error = %v", err)
	}
	frame, err := clientSession.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(PONG) error = %v", err)
	}
	if frame.Type != protocol.TypePong || len(frame.Payload) != 0 {
		t.Fatalf("heartbeat response = {%s %q}, want {PONG empty}", frame.Type, frame.Payload)
	}

	cancel()
	if exitCode := serveCLIReceive(t, result, "serve shutdown"); exitCode != 0 {
		t.Fatalf("RunContext(serve) exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Node ID: "+serverIdentity.NodeID()) {
		t.Fatalf("stdout = %q, want server node ID", stdout.String())
	}
	if !strings.Contains(stdout.String(), "GoBridge server stopped") {
		t.Fatalf("stdout = %q, want stop message", stdout.String())
	}
}

func serveCLITestNode(t *testing.T, role config.Role, controlListen string) string {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.New(role)
	if err != nil {
		t.Fatalf("config.New() error = %v", err)
	}
	if role == config.RoleServer && controlListen != "" {
		cfg.Server.ControlListen = controlListen
	}
	if err := config.SaveNew(dir, cfg); err != nil {
		t.Fatalf("config.SaveNew() error = %v", err)
	}
	nodeIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	if err := identity.SaveNew(dir, nodeIdentity); err != nil {
		t.Fatalf("identity.SaveNew() error = %v", err)
	}
	return dir
}

func serveCLIListeningAddress(t *testing.T, output string) string {
	t.Helper()
	const prefix = "GoBridge server listening on "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("listening address missing from output %q", output)
	return ""
}

func serveCLIEventually(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(serveCLITestTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func serveCLIReceive[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(serveCLITestTimeout):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
