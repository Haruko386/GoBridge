package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
)

func runStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configDir := flags.String("config-dir", "", "configuration directory (default: ~/.gobridge)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gobridge status [--config-dir <path>]")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "status: unexpected arguments: %v\n", flags.Args())
		return 2
	}

	dir, err := resolveConfigDirectory(*configDir)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}
	cfg, err := config.Load(dir)
	if err != nil {
		fmt.Fprintf(stderr, "status: load configuration: %v\n", err)
		return 1
	}
	nodeIdentity, err := identity.Load(dir)
	if err != nil {
		fmt.Fprintf(stderr, "status: load identity: %v\n", err)
		return 1
	}
	store, err := peer.Open(dir)
	if err != nil {
		fmt.Fprintf(stderr, "status: open peer store: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Role: %s\n", cfg.Role)
	fmt.Fprintf(stdout, "Node ID: %s\n", nodeIdentity.NodeID())
	fmt.Fprintf(stdout, "Peers: %d\n", len(store.List()))

	if cfg.Role == config.RoleServer {
		fmt.Fprintf(stdout, "Control listen: %s\n", cfg.Server.ControlListen)
		fmt.Fprintf(stdout, "Proxy listen: %s\n", cfg.Server.ProxyListen)
		return 0
	}

	fmt.Fprintf(stdout, "Upstream proxy: %s\n", cfg.Client.ProxyAddress)
	if cfg.Client.ServerAddress == "" {
		fmt.Fprintln(stdout, "Server: unpaired")
		return 0
	}

	state := "missing"
	if paired, err := store.Get(cfg.Client.ServerNodeID); err == nil {
		if paired.Enabled {
			state = "enabled"
		} else {
			state = "disabled"
		}
	}
	fmt.Fprintf(stdout, "Server: %s\n", cfg.Client.ServerAddress)
	fmt.Fprintf(stdout, "Server Node ID: %s\n", cfg.Client.ServerNodeID)
	fmt.Fprintf(stdout, "Server peer: %s\n", state)
	return 0
}

func runPeers(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("peers", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configDir := flags.String("config-dir", "", "configuration directory (default: ~/.gobridge)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gobridge peers [--config-dir <path>]")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "peers: unexpected arguments: %v\n", flags.Args())
		return 2
	}

	dir, err := resolveConfigDirectory(*configDir)
	if err != nil {
		fmt.Fprintf(stderr, "peers: %v\n", err)
		return 1
	}
	store, err := peer.Open(dir)
	if err != nil {
		fmt.Fprintf(stderr, "peers: open peer store: %v\n", err)
		return 1
	}
	peers := store.List()
	if len(peers) == 0 {
		fmt.Fprintln(stdout, "No peers.")
		return 0
	}

	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSTATUS\tNODE ID")
	for _, paired := range peers {
		status := "disabled"
		if paired.Enabled {
			status = "enabled"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\n", paired.Name, status, paired.NodeID)
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintf(stderr, "peers: write output: %v\n", err)
		return 1
	}
	return 0
}

func runPeerCommand(command string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "Usage: gobridge %s <peer> [--config-dir <path>]\n", command)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(stdout, "Usage: gobridge %s <peer> [--config-dir <path>]\n", command)
		return 0
	}

	target := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configDir := flags.String("config-dir", "", "configuration directory (default: ~/.gobridge)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s: unexpected arguments: %v\n", command, flags.Args())
		return 2
	}

	dir, err := resolveConfigDirectory(*configDir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command, err)
		return 1
	}
	store, err := peer.Open(dir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: open peer store: %v\n", command, err)
		return 1
	}
	paired, err := findPeer(store.List(), target)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command, err)
		return 1
	}

	switch command {
	case "enable":
		err = store.Enable(paired.NodeID)
	case "disable":
		err = store.Disable(paired.NodeID)
	case "unpair":
		err = removePairing(dir, store, paired)
	default:
		err = fmt.Errorf("unsupported peer command %q", command)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command, err)
		return 1
	}

	fmt.Fprintf(stdout, "%s: %s (%s)\n", command, paired.Name, paired.NodeID)
	return 0
}

func findPeer(peers []peer.Peer, target string) (peer.Peer, error) {
	for _, paired := range peers {
		if paired.NodeID == target || paired.Name == target {
			return paired, nil
		}
	}
	return peer.Peer{}, fmt.Errorf("%w: %s", peer.ErrNotFound, target)
}

func removePairing(dir string, store *peer.Store, paired peer.Peer) error {
	cfg, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	clearServer := cfg.Role == config.RoleClient && cfg.Client.ServerNodeID == paired.NodeID
	previousAddress := ""
	previousNodeID := ""
	if clearServer {
		previousAddress = cfg.Client.ServerAddress
		previousNodeID = cfg.Client.ServerNodeID
		cfg.Client.ServerAddress = ""
		cfg.Client.ServerNodeID = ""
		if err := config.Save(dir, cfg); err != nil {
			return fmt.Errorf("clear paired server configuration: %w", err)
		}
	}

	if err := store.Remove(paired.NodeID); err != nil {
		if clearServer {
			cfg.Client.ServerAddress = previousAddress
			cfg.Client.ServerNodeID = previousNodeID
			err = errors.Join(err, config.Save(dir, cfg))
		}
		return fmt.Errorf("remove peer: %w", err)
	}
	return nil
}

func resolveConfigDirectory(value string) (string, error) {
	if value != "" {
		return value, nil
	}
	return config.DefaultDir()
}
