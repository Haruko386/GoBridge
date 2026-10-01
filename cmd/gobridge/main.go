package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Haruko386/GoBridge/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exitCode := cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr, version)
	os.Exit(exitCode)
}
