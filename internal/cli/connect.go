package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/Haruko386/GoBridge/internal/client"
	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/transport"
)

const (
	defaultHeartbeatInterval = 10 * time.Second
	defaultHeartbeatTimeout  = 10 * time.Second
	defaultMinBackoff        = time.Second
	defaultMaxBackoff        = 30 * time.Second
	defaultProxyDialTimeout  = 10 * time.Second
)

func runConnect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)

	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gobridge connect [options]")
		flags.PrintDefaults()
	}

	configDir := flags.String(
		"config-dir",
		"",
		"configuration directory (default: ~/.gobridge)",
	)

	authTimeout := flags.Duration(
		"auth-timeout",
		transport.DefaultHandshakeTimeout,
		"authenticated connection handshake timeout",
	)

	heartbeatInterval := flags.Duration(
		"heartbeat-interval",
		defaultHeartbeatInterval,
		"delay between successful heartbeats",
	)

	heartbeatTimeout := flags.Duration(
		"heartbeat-timeout",
		defaultHeartbeatTimeout,
		"maximum time to wait for a heartbeat response",
	)

	minBackoff := flags.Duration(
		"min-backoff",
		defaultMinBackoff,
		"initial reconnect delay",
	)

	maxBackoff := flags.Duration(
		"max-backoff",
		defaultMaxBackoff,
		"maximum reconnect delay",
	)

	proxyDialTimeout := flags.Duration("proxy-dial-timeout", defaultProxyDialTimeout, "maximum time to connect to the local proxy")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "connect: unexpected arguments: %v\n", flags.Args())
		return 2
	}

	if *authTimeout <= 0 || *heartbeatInterval <= 0 || *heartbeatTimeout <= 0 || *minBackoff <= 0 || *maxBackoff <= 0 || *proxyDialTimeout <= 0 {
		fmt.Fprintln(stderr, "connect: all durations must be positive")
		return 2
	}

	if *maxBackoff < *minBackoff {
		fmt.Fprintln(stderr, "connect: --max-backoff must not be less than --min-backoff")
		return 2
	}

	var err error

	if *configDir == "" {
		*configDir, err = config.DefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "connect: determine configuration directory: %v\n", err)
			return 1
		}
	}

	cfg, err := config.Load(*configDir)
	if err != nil {
		fmt.Fprintln(stderr, "connect: failed to load configuration:", err)
		return 1
	}

	if cfg.Role != config.RoleClient {
		fmt.Fprintf(stderr, "connect: configured role is %q, want %q\n", cfg.Role, config.RoleClient)
		return 1
	}

	nodeIdentity, err := identity.Load(*configDir)
	if err != nil {
		fmt.Fprintln(stderr, "connect: failed to load node identity:", err)
		return 1
	}

	peers, err := peer.Open(*configDir)
	if err != nil {
		fmt.Fprintln(stderr, "connect: failed to open peers:", err)
		return 1
	}

	connector, err := client.NewConnector(cfg, nodeIdentity, peers, *authTimeout)
	if err != nil {
		fmt.Fprintln(stderr, "connect: failed to create connector:", err)
		return 1
	}

	logger := log.New(stderr, "connect: ", 0)
	forwarder, err := client.NewProxyForwarder(cfg.Client.ProxyAddress, *proxyDialTimeout)
	if err != nil {
		fmt.Fprintln(stderr, "connect: failed to create proxy forwarder:", err)
		return 1
	}

	runner, err := client.NewTunnelRunner(connector, forwarder, *heartbeatInterval, *heartbeatTimeout, *minBackoff, *maxBackoff, func(err error) {
		logger.Printf("%v", err)
	})
	if err != nil {
		fmt.Fprintln(stderr, "connect: failed to create runner:", err)
		return 1
	}

	fmt.Fprintf(stdout, "GoBridge client connecting to %s\n", connector.Address())
	fmt.Fprintf(stdout, "Expected server Node ID: %s\n", connector.ServerNodeID())
	fmt.Fprintf(stdout, "Upstream proxy: %s\n", forwarder.Address())

	if err := runner.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "connect: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "GoBridge client stopped")
	return 0
}
