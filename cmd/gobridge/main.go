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
	go func() {
		<-ctx.Done()
		stop()
	}()

	exitCode := cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr, version)
	stop()
	os.Exit(exitCode)
}
