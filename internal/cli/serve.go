package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/Haruko386/GoBridge/internal/config"
	"github.com/Haruko386/GoBridge/internal/identity"
	"github.com/Haruko386/GoBridge/internal/peer"
	"github.com/Haruko386/GoBridge/internal/server"
	"github.com/Haruko386/GoBridge/internal/transport"
)

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)

	configDir := flags.String("config-dir", "", "configuration directory (default: ~/.gobridge)")

	authTimeout := flags.Duration("auth-timeout", transport.DefaultHandshakeTimeout, "authenticated connection handshake timeout")

	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gobridge serve [--config-dir <path>] [--auth-timeout <duration>]")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "serve: unexpected arguments: %v\n", flags.Args())
		return 2
	}

	if *authTimeout <= 0 {
		fmt.Fprintln(stderr, "serve: --auth-timeout must be positive")
		return 2
	}

	var err error

	if *configDir == "" {
		*configDir, err = config.DefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "serve: determine configuration directory: %v\n", err)
			return 1
		}
	}

	cfg, err := config.Load(*configDir)
	if err != nil {
		fmt.Fprintf(stderr, "serve: load configuration: %v\n", err)
		return 1
	}

	if cfg.Role != config.RoleServer {
		fmt.Fprintf(stderr, "serve: configured role is %q, want %q\n", cfg.Role, config.RoleServer)
		return 1
	}

	nodeIdentity, err := identity.Load(*configDir)
	if err != nil {
		fmt.Fprintf(stderr, "serve: load identity: %v\n", err)
		return 1
	}

	peers, err := peer.Open(*configDir)
	if err != nil {
		fmt.Fprintf(stderr, "serve: open peer store: %v\n", err)
		return 1
	}

	listener, err := net.Listen(
		"tcp",
		cfg.Server.ControlListen,
	)
	if err != nil {
		fmt.Fprintf(stderr, "serve: listen on %s: %v\n", cfg.Server.ControlListen, err)
		return 1
	}

	registry := server.NewRegistry()

	logger := log.New(stderr, "serve: ", 0)

	runtimeServer, err := server.NewServer(
		listener,
		nodeIdentity,
		peers.PublicKey,
		registry,
		*authTimeout,
		server.HandleHeartbeatSession,
		func(err error) {
			logger.Printf("connection error: %v", err)
		},
	)
	if err != nil {
		_ = listener.Close()

		fmt.Fprintf(stderr, "serve: create server runtime: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "GoBridge server listening on %s\n", listener.Addr())
	fmt.Fprintf(stdout, "Node ID: %s\n", nodeIdentity.NodeID())

	if err := runtimeServer.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "GoBridge server stopped")
	return 0
}
