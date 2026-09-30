package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func TestPairCLIEndToEnd(t *testing.T) {
	serverDir := t.TempDir()
	clientDir := t.TempDir()
	initializePairingNode(t, serverDir, config.RoleServer)
	initializePairingNode(t, clientDir, config.RoleClient)

	serverConfig, err := config.Load(serverDir)
	if err != nil {
		t.Fatalf("config.Load(server) error = %v", err)
	}
	serverConfig.Server.ControlListen = "127.0.0.1:0"
	if err := config.Save(serverDir, serverConfig); err != nil {
		t.Fatalf("config.Save(server) error = %v", err)
	}

	serverStdout := newObservableBuffer()
	var serverStderr bytes.Buffer
	serverExit := make(chan int, 1)
	go func() {
		serverExit <- Run(
			[]string{
				"pair", "create",
				"--config-dir", serverDir,
				"--name", "machine-room-server",
				"--ttl", "5s",
				"--timeout", "2s",
			},
			serverStdout,
			&serverStderr,
			"test-version",
		)
	}()

	output := serverStdout.waitFor(t, "Expires in:", 3*time.Second)
	code := outputLineValue(t, output, "Pair code:")
	address := outputLineValue(t, output, "Listening:")

	var clientStdout bytes.Buffer
	var clientStderr bytes.Buffer
	clientExit := Run(
		[]string{
			"pair", address,
			"--code", code,
			"--config-dir", clientDir,
			"--name", "lab-client",
			"--timeout", "2s",
		},
		&clientStdout,
		&clientStderr,
		"test-version",
	)
	if clientExit != 0 {
		t.Fatalf("client exit code = %d; stderr = %q", clientExit, clientStderr.String())
	}

	select {
	case exitCode := <-serverExit:
		if exitCode != 0 {
			t.Fatalf("server exit code = %d; stderr = %q", exitCode, serverStderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pair create did not finish")
	}

	serverIdentity, err := identity.Load(serverDir)
	if err != nil {
		t.Fatalf("identity.Load(server) error = %v", err)
	}
	clientIdentity, err := identity.Load(clientDir)
	if err != nil {
		t.Fatalf("identity.Load(client) error = %v", err)
	}
	serverStore, err := peer.Open(serverDir)
	if err != nil {
		t.Fatalf("peer.Open(server) error = %v", err)
	}
	clientStore, err := peer.Open(clientDir)
	if err != nil {
		t.Fatalf("peer.Open(client) error = %v", err)
	}
	if _, err := serverStore.Get(clientIdentity.NodeID()); err != nil {
		t.Fatalf("server store Get(client) error = %v", err)
	}
	if _, err := clientStore.Get(serverIdentity.NodeID()); err != nil {
		t.Fatalf("client store Get(server) error = %v", err)
	}

	clientConfig, err := config.Load(clientDir)
	if err != nil {
		t.Fatalf("config.Load(client) error = %v", err)
	}
	if clientConfig.Client.ServerAddress != address {
		t.Fatalf("saved server address = %q, want %q", clientConfig.Client.ServerAddress, address)
	}
	if clientConfig.Client.ServerNodeID != serverIdentity.NodeID() {
		t.Fatalf(
			"saved server node ID = %q, want %q",
			clientConfig.Client.ServerNodeID,
			serverIdentity.NodeID(),
		)
	}
}

func TestPairCLIRejectsInvalidCodeWithoutEchoingSecret(t *testing.T) {
	const invalidCode = "secret-code-that-must-not-be-logged"
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run(
		[]string{"pair", "127.0.0.1:18790", "--code", invalidCode},
		&stdout,
		&stderr,
		"test-version",
	)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if strings.Contains(stderr.String(), invalidCode) {
		t.Fatalf("stderr exposed pair code: %q", stderr.String())
	}
}

func TestResolveNodeNameRejectsWhitespace(t *testing.T) {
	if _, err := resolveNodeName(" client "); err == nil {
		t.Fatal("resolveNodeName() error = nil")
	}
}

func initializePairingNode(t *testing.T, dir string, role config.Role) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(
		[]string{"init", "--role", string(role), "--config-dir", dir},
		&stdout,
		&stderr,
		"test-version",
	)
	if exitCode != 0 {
		t.Fatalf("init %s exit code = %d; stderr = %q", role, exitCode, stderr.String())
	}
}

type observableBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	notify chan struct{}
}

func newObservableBuffer() *observableBuffer {
	return &observableBuffer{notify: make(chan struct{}, 1)}
}

func (b *observableBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	written, err := b.buffer.Write(data)
	b.mu.Unlock()

	select {
	case b.notify <- struct{}{}:
	default:
	}
	return written, err
}

func (b *observableBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *observableBuffer) waitFor(t *testing.T, text string, timeout time.Duration) string {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		if output := b.String(); strings.Contains(output, text) {
			return output
		}
		select {
		case <-b.notify:
		case <-timer.C:
			t.Fatalf("timed out waiting for %q in output %q", text, b.String())
		}
	}
}

func outputLineValue(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("output %q has no %q line", output, prefix)
	return ""
}
