package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	clientruntime "github.com/Haruko386/GoBridge/internal/client"
	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func TestRunConnectUsageErrors(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{name: "help", args: []string{"connect", "--help"}, wantExit: 0, wantStderr: "Usage: gobridge connect"},
		{name: "unknown flag", args: []string{"connect", "--unknown"}, wantExit: 2, wantStderr: "flag provided but not defined"},
		{name: "unexpected argument", args: []string{"connect", "extra"}, wantExit: 2, wantStderr: "unexpected arguments"},
		{name: "zero auth timeout", args: []string{"connect", "--auth-timeout", "0s"}, wantExit: 2, wantStderr: "all durations must be positive"},
		{name: "zero heartbeat interval", args: []string{"connect", "--heartbeat-interval", "0s"}, wantExit: 2, wantStderr: "all durations must be positive"},
		{name: "backoff bounds reversed", args: []string{"connect", "--min-backoff", "2s", "--max-backoff", "1s"}, wantExit: 2, wantStderr: "must not be less"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := RunContext(context.Background(), test.args, &stdout, &stderr, "test-version")
			if exitCode != test.wantExit {
				t.Fatalf("RunContext() exit code = %d, want %d", exitCode, test.wantExit)
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestRunConnectRejectsServerConfiguration(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleServer, "127.0.0.1:0")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := RunContext(
		context.Background(),
		[]string{"connect", "--config-dir", dir},
		&stdout,
		&stderr,
		"test-version",
	)
	if exitCode != 1 {
		t.Fatalf("RunContext(connect server) exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), `configured role is "server"`) {
		t.Fatalf("stderr = %q, want server-role error", stderr.String())
	}
}

func TestRunConnectRejectsUnpairedClient(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleClient, "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := RunContext(
		context.Background(),
		[]string{"connect", "--config-dir", dir},
		&stdout,
		&stderr,
		"test-version",
	)
	if exitCode != 1 {
		t.Fatalf("RunContext(unpaired client) exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), clientruntime.ErrNotPaired.Error()) {
		t.Fatalf("stderr = %q, want ErrNotPaired", stderr.String())
	}
}

func TestServeAndConnectCLIEndToEnd(t *testing.T) {
	serverDir := serveCLITestNode(t, config.RoleServer, "127.0.0.1:0")
	clientDir := serveCLITestNode(t, config.RoleClient, "")
	serverIdentity := connectCLILoadIdentity(t, serverDir)
	clientIdentity := connectCLILoadIdentity(t, clientDir)
	connectCLIAddPeer(t, serverDir, "test-client", clientIdentity)

	serverCtx, stopServer := context.WithCancel(context.Background())
	var serverStdout synchronizedBuffer
	var serverStderr synchronizedBuffer
	serverResult := make(chan int, 1)
	go func() {
		serverResult <- RunContext(
			serverCtx,
			[]string{"serve", "--config-dir", serverDir, "--auth-timeout", "1s"},
			&serverStdout,
			&serverStderr,
			"test-version",
		)
	}()
	defer stopServer()
	serveCLIEventually(t, func() bool {
		return strings.Contains(serverStdout.String(), "GoBridge server listening on ")
	}, "server CLI startup")
	serverAddress := serveCLIListeningAddress(t, serverStdout.String())

	clientConfig, err := config.Load(clientDir)
	if err != nil {
		t.Fatalf("config.Load(client) error = %v", err)
	}
	clientConfig.Client.ServerAddress = serverAddress
	clientConfig.Client.ServerNodeID = serverIdentity.NodeID()
	if err := config.Save(clientDir, clientConfig); err != nil {
		t.Fatalf("config.Save(client) error = %v", err)
	}
	connectCLIAddPeer(t, clientDir, "test-server", serverIdentity)

	clientCtx, stopClient := context.WithCancel(context.Background())
	var clientStdout synchronizedBuffer
	var clientStderr synchronizedBuffer
	clientResult := make(chan int, 1)
	go func() {
		clientResult <- RunContext(
			clientCtx,
			[]string{
				"connect",
				"--config-dir", clientDir,
				"--auth-timeout", "1s",
				"--heartbeat-interval", "5ms",
				"--heartbeat-timeout", "200ms",
				"--min-backoff", "5ms",
				"--max-backoff", "20ms",
			},
			&clientStdout,
			&clientStderr,
			"test-version",
		)
	}()
	defer stopClient()

	serveCLIEventually(t, func() bool {
		return strings.Contains(clientStdout.String(), "GoBridge client connecting to ")
	}, "client CLI startup")
	if !strings.Contains(clientStdout.String(), "Expected server Node ID: "+serverIdentity.NodeID()) {
		t.Fatalf("client stdout = %q, want expected server node ID", clientStdout.String())
	}

	// Stopping the server after the client has had time to exchange heartbeats
	// makes the client's connection-ended log observable and proves the two CLI
	// runtimes established a real authenticated session.
	time.Sleep(25 * time.Millisecond)
	stopServer()
	if exitCode := serveCLIReceive(t, serverResult, "server CLI shutdown"); exitCode != 0 {
		t.Fatalf("server exit code = %d, want 0; stderr = %q", exitCode, serverStderr.String())
	}
	serveCLIEventually(t, func() bool {
		return strings.Contains(clientStderr.String(), "connection ended")
	}, "client detection of server shutdown")

	stopClient()
	if exitCode := serveCLIReceive(t, clientResult, "client CLI shutdown"); exitCode != 0 {
		t.Fatalf("client exit code = %d, want 0; stderr = %q", exitCode, clientStderr.String())
	}
	if !strings.Contains(clientStdout.String(), "GoBridge client stopped") {
		t.Fatalf("client stdout = %q, want stop message", clientStdout.String())
	}
}

func connectCLILoadIdentity(t *testing.T, dir string) identity.Identity {
	t.Helper()
	value, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("identity.Load(%s) error = %v", dir, err)
	}
	return value
}

func connectCLIAddPeer(t *testing.T, dir, name string, nodeIdentity identity.Identity) {
	t.Helper()
	store, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open(%s) error = %v", dir, err)
	}
	value, err := peer.New(name, nodeIdentity.PublicKey())
	if err != nil {
		t.Fatalf("peer.New(%s) error = %v", name, err)
	}
	if err := store.Add(value); err != nil {
		t.Fatalf("Store.Add(%s) error = %v", name, err)
	}
}
