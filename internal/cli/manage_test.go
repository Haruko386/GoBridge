package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func TestStatusAndPeersCommands(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleServer, "127.0.0.1:0")
	pairedIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	connectCLIAddPeer(t, dir, "lab-client", pairedIdentity)

	stdout, stderr, exitCode := runManagementCommand(t, "status", "--config-dir", dir)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("status = (%d, %q), stderr = %q", exitCode, stdout, stderr)
	}
	for _, value := range []string{"Role: server", "Peers: 1", "Proxy listen: 127.0.0.1:0"} {
		if !strings.Contains(stdout, value) {
			t.Fatalf("status output = %q, want %q", stdout, value)
		}
	}

	stdout, stderr, exitCode = runManagementCommand(t, "peers", "--config-dir", dir)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("peers = (%d, %q), stderr = %q", exitCode, stdout, stderr)
	}
	for _, value := range []string{"NAME", "STATUS", "NODE ID", "lab-client", "enabled", pairedIdentity.NodeID()} {
		if !strings.Contains(stdout, value) {
			t.Fatalf("peers output = %q, want %q", stdout, value)
		}
	}
}

func TestEnableDisableAndUnpairCommands(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleServer, "127.0.0.1:0")
	pairedIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	connectCLIAddPeer(t, dir, "lab-client", pairedIdentity)

	_, stderr, exitCode := runManagementCommand(t, "disable", "lab-client", "--config-dir", dir)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("disable exit code = %d, stderr = %q", exitCode, stderr)
	}
	store, err := peer.Open(dir)
	if err != nil {
		t.Fatalf("peer.Open() error = %v", err)
	}
	paired, err := store.Get(pairedIdentity.NodeID())
	if err != nil || paired.Enabled {
		t.Fatalf("disabled peer = (%#v, %v)", paired, err)
	}

	_, stderr, exitCode = runManagementCommand(t, "enable", pairedIdentity.NodeID(), "--config-dir", dir)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("enable exit code = %d, stderr = %q", exitCode, stderr)
	}
	store, _ = peer.Open(dir)
	paired, err = store.Get(pairedIdentity.NodeID())
	if err != nil || !paired.Enabled {
		t.Fatalf("enabled peer = (%#v, %v)", paired, err)
	}

	_, stderr, exitCode = runManagementCommand(t, "unpair", "lab-client", "--config-dir", dir)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("unpair exit code = %d, stderr = %q", exitCode, stderr)
	}
	store, _ = peer.Open(dir)
	if _, err := store.Get(pairedIdentity.NodeID()); !errors.Is(err, peer.ErrNotFound) {
		t.Fatalf("Get(unpaired) error = %v, want ErrNotFound", err)
	}
}

func TestClientUnpairClearsServerConfiguration(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleClient, "")
	serverIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error = %v", err)
	}
	connectCLIAddPeer(t, dir, "server", serverIdentity)
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	cfg.Client.ServerAddress = "127.0.0.1:18790"
	cfg.Client.ServerNodeID = serverIdentity.NodeID()
	if err := config.Save(dir, cfg); err != nil {
		t.Fatalf("config.Save() error = %v", err)
	}

	_, stderr, exitCode := runManagementCommand(t, "unpair", "server", "--config-dir", dir)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("unpair exit code = %d, stderr = %q", exitCode, stderr)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load(after unpair) error = %v", err)
	}
	if cfg.Client.ServerAddress != "" || cfg.Client.ServerNodeID != "" {
		t.Fatalf("client server configuration = (%q, %q), want empty", cfg.Client.ServerAddress, cfg.Client.ServerNodeID)
	}
}

func TestPeerCommandsReportUnknownPeer(t *testing.T) {
	dir := serveCLITestNode(t, config.RoleServer, "127.0.0.1:0")
	_, stderr, exitCode := runManagementCommand(t, "disable", "missing", "--config-dir", dir)
	if exitCode != 1 || !strings.Contains(stderr, peer.ErrNotFound.Error()) {
		t.Fatalf("disable missing = (%d, %q)", exitCode, stderr)
	}
}

func runManagementCommand(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(args, &stdout, &stderr, "test-version")
	return stdout.String(), stderr.String(), exitCode
}
